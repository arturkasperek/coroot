package prom

import (
	"context"
	"fmt"
	"sort"
	"strings"

	chgo "github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/ch-go/proto"
	"github.com/coroot/coroot/ch"
	promModel "github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
)

type clickhouseQuerier struct {
	ch         *ch.LowLevelClient
	mint, maxt int64
}

// selectSQL reads the samples of the series that match, one row per series.
//
// Labels live in metrics_series, once per series, and samples in
// metrics_samples without them. Matchers on labels are resolved on the small
// series table first (MetricHash IN (...)), so the samples query never looks at
// a label; the metric name is in both tables and filters both.
func (q *clickhouseQuerier) selectSQL(matchers []*labels.Matcher) string {
	nameConds, seriesConds := q.conditions(matchers)
	seriesWhere := strings.Join(append(append([]string{}, nameConds...), seriesConds...), " AND ")
	if seriesWhere == "" {
		seriesWhere = "1"
	}
	samplesWhere := fmt.Sprintf("Timestamp >= toDateTime(%d) AND Timestamp <= toDateTime(%d)", q.mint/1000, q.maxt/1000)
	for _, c := range nameConds {
		samplesWhere += " AND " + c
	}
	if len(seriesConds) > 0 {
		samplesWhere += " AND MetricHash IN (SELECT MetricHash FROM @@table_metrics_series@@ WHERE " + seriesWhere + ")"
	}
	// any() because ReplacingMergeTree may not have merged the repeats of a
	// series yet; the aliases differ from the column names so that they cannot
	// be read as aggregates inside WHERE.
	return fmt.Sprintf(`
		SELECT
		    s.mn AS MetricName,
		    s.lbl AS Labels,
		    d.Timestamps AS Timestamps,
		    d.Values AS Values
		FROM (
		    SELECT MetricHash,
		        groupArray(toUnixTimestamp(Timestamp)) AS Timestamps,
		        groupArray(Value) AS Values
		    FROM @@table_metrics_samples@@
		    WHERE %s
		    GROUP BY MetricHash
		) AS d
		INNER JOIN (
		    SELECT MetricHash, any(MetricName) AS mn, any(Labels) AS lbl
		    FROM @@table_metrics_series@@
		    WHERE %s
		    GROUP BY MetricHash
		) AS s USING MetricHash
		ORDER BY s.mn, s.lbl
	`, samplesWhere, seriesWhere)
}

func (q *clickhouseQuerier) Select(ctx context.Context, _ bool, hints *storage.SelectHints, matchers ...*labels.Matcher) storage.SeriesSet {
	query := q.selectSQL(matchers)

	metricName := &proto.ColStr{}                                                  // any() of a LowCardinality column is a plain String
	metricLabels := proto.NewMap[string, string](&proto.ColStr{}, &proto.ColStr{}) // plain Strings, see above
	timestamps := proto.NewArray[uint32](&proto.ColUInt32{})
	values := proto.NewArray[float64](&proto.ColFloat64{})

	ss := &seriesSet{}
	ss.err = q.ch.Do(ctx, chgo.Query{
		Body: query,
		Result: proto.Results{
			{Name: "MetricName", Data: metricName},
			{Name: "Labels", Data: metricLabels},
			{Name: "Timestamps", Data: timestamps},
			{Name: "Values", Data: values},
		},
		OnResult: func(ctx context.Context, block proto.Block) error {
			for i := 0; i < block.Rows; i++ {
				lsMap := metricLabels.Row(i)
				ls := make(labels.Labels, 0, len(lsMap)+1)
				ls = append(ls, labels.Label{Name: promModel.MetricNameLabel, Value: metricName.Row(i)})
				for k, v := range lsMap {
					ls = append(ls, labels.Label{Name: k, Value: v})
				}
				sort.Slice(ls, func(i, j int) bool { return ls[i].Name < ls[j].Name })
				tss := timestamps.Row(i)
				vals := values.Row(i)
				samples := make([]chunks.Sample, 0, len(tss))
				for j, ts := range tss {
					samples = append(samples, sample{t: (int64(ts) * 1000) * hints.Step / hints.Step, f: vals[j]})
				}
				ss.series = append(ss.series, storage.NewListSeries(ls, sortedAndDeduplicatedSamples(samples)))
			}
			return nil
		},
	})
	return ss
}

func sortedAndDeduplicatedSamples(samples []chunks.Sample) []chunks.Sample {
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].T() < samples[j].T()
	})
	deduped := samples[:0]
	for i := 0; i < len(samples); {
		t := samples[i].T()
		j := i + 1
		for j < len(samples) && samples[j].T() == t {
			j++
		}
		deduped = append(deduped, samples[j-1])
		i = j
	}
	return deduped
}

// labelValuesSQL reads label values from the series table: a series is listed
// while any sample of it arrived since the start of the window.
func (q *clickhouseQuerier) labelValuesSQL(name string, matchers []*labels.Matcher) string {
	nameConds, seriesConds := q.conditions(matchers)
	conds := append([]string{fmt.Sprintf("LastSeen >= toDateTime(%d)", q.mint/1000)}, nameConds...)
	conds = append(conds, seriesConds...)
	column := "Labels['" + escapeString(name) + "']"
	if name == promModel.MetricNameLabel {
		column = "MetricName"
	}
	return fmt.Sprintf("SELECT DISTINCT %s as LabelValue FROM @@table_metrics_series@@ WHERE %s", column, strings.Join(conds, " AND "))
}

func (q *clickhouseQuerier) LabelValues(ctx context.Context, name string, _ *storage.LabelHints, matchers ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	var res []string
	if name == promModel.MetricNameLabel {
		value := (&proto.ColStr{}).LowCardinality()
		err := q.ch.Do(ctx, chgo.Query{
			Body:   q.labelValuesSQL(name, matchers),
			Result: proto.Results{{Name: "LabelValue", Data: value}},
			OnResult: func(ctx context.Context, block proto.Block) error {
				for i := 0; i < block.Rows; i++ {
					res = append(res, value.Row(i))
				}
				return nil
			},
		})
		return res, nil, err
	}

	value := &proto.ColStr{}
	err := q.ch.Do(ctx, chgo.Query{
		Body:   q.labelValuesSQL(name, matchers),
		Result: proto.Results{{Name: "LabelValue", Data: value}},
		OnResult: func(ctx context.Context, block proto.Block) error {
			for i := 0; i < block.Rows; i++ {
				res = append(res, value.Row(i))
			}
			return nil
		},
	})
	return res, nil, err
}

func (q *clickhouseQuerier) LabelNames(ctx context.Context, _ *storage.LabelHints, _ ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	return nil, nil, fmt.Errorf("not yet implemented")
}

func (q *clickhouseQuerier) Close() error {
	return nil
}

// conditions splits the matchers: those on the metric name apply to both
// tables, those on labels only to the series table.
func (q *clickhouseQuerier) conditions(ms []*labels.Matcher) (nameConds, seriesConds []string) {
	for _, m := range ms {
		c := q.condition(m)
		if c == "" {
			continue
		}
		if m.Name == labels.MetricName {
			nameConds = append(nameConds, c)
		} else {
			seriesConds = append(seriesConds, c)
		}
	}
	return
}

func (q *clickhouseQuerier) condition(matcher *labels.Matcher) string {
	var column string
	if matcher.Name == labels.MetricName {
		column = "MetricName"
	} else {
		column = "Labels['" + escapeString(matcher.Name) + "']"
	}
	switch matcher.Type {
	case labels.MatchEqual:
		return fmt.Sprintf("%s = '%s'", column, escapeString(matcher.Value))
	case labels.MatchNotEqual:
		return fmt.Sprintf("%s != '%s'", column, escapeString(matcher.Value))
	case labels.MatchRegexp:
		return fmt.Sprintf("match(%s, '%s')", column, escapeString(matcher.Value))
	case labels.MatchNotRegexp:
		return fmt.Sprintf("NOT match(%s, '%s')", column, escapeString(matcher.Value))
	}
	return ""
}

type seriesSet struct {
	err    error
	cur    int
	series []storage.Series
}

func (ss *seriesSet) Next() bool {
	ss.cur++
	return ss.cur-1 < len(ss.series)
}

func (ss *seriesSet) At() storage.Series {
	return ss.series[ss.cur-1]
}

func (ss *seriesSet) Err() error {
	return ss.err
}

func (ss *seriesSet) Warnings() annotations.Annotations { return nil }

type sample struct {
	t int64
	f float64
}

func (s sample) T() int64 {
	return s.t
}

func (s sample) F() float64 {
	return s.f
}

func (s sample) H() *histogram.Histogram {
	return nil
}

func (s sample) FH() *histogram.FloatHistogram {
	return nil
}

func (s sample) Type() chunkenc.ValueType {
	return chunkenc.ValFloat
}

func (s sample) Copy() chunks.Sample {
	return sample{t: s.t, f: s.f}
}

func escapeString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

package promql

import (
	"context"
	"fmt"
	"strings"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	promModel "github.com/prometheus/common/model"
	"golang.org/x/exp/maps"
)

// FilterLabelsF says which labels of a series are kept.
type FilterLabelsF func(name string) bool

func FilterLabelsKeepAll(string) bool { return true }
func FilterLabelsDropAll(string) bool { return false }

// metricsTable is the TimeSeries table (see ch.MetricsCluster).
const metricsTable = "metrics"

// Client evaluates PromQL on the metrics table of a project's ClickHouse.
type Client struct {
	c    *clickhouse.Client
	step timeseries.Duration
}

// New: step is the scrape interval, what GetStep reports.
func New(c *clickhouse.Client, step timeseries.Duration) *Client {
	return &Client{c: c, step: step}
}

func (c *Client) GetStep(from, to timeseries.Time) (timeseries.Duration, error) {
	return c.step, nil
}

func (c *Client) Ping(ctx context.Context) error {
	now := timeseries.Now()
	_, err := c.QueryRange(ctx, "up", FilterLabelsDropAll, now.Add(-timeseries.Hour), now, timeseries.Minute)
	return err
}

// Close does nothing: the ClickHouse client belongs to the caller.
func (c *Client) Close() {}

type rawSeries struct {
	tags map[string]string // __name__ included, empty labels omitted by ClickHouse
	ts   []uint32          // unix seconds
	vs   []float64
}

// queryRange evaluates query for every step of [from, to]. A series that ends
// inside the range has NaN points after its end; callers drop them.
//
// tags and samples are Array(Tuple(...)) in ClickHouse. clickhouse-go can scan
// them into [][]any, but not the (DateTime64, Float64) tuple of samples into a
// typed struct (checked on v2.8.3 and v2.48.0), and [][]any boxes every point.
// So the query splits them into typed arrays: names and values of the labels,
// unix times and values of the samples.
func (c *Client) queryRange(ctx context.Context, query string, from, to, step int64) ([]rawSeries, error) {
	rows, err := c.c.Query(ch.TimeSeriesContext(ctx),
		`SELECT arrayMap(x -> x.1, tags) AS names, arrayMap(x -> x.2, tags) AS vals,
		        arrayMap(p -> toUnixTimestamp(p.1), samples) AS ts, arrayMap(p -> p.2, samples) AS vs
		 FROM prometheusQueryRange(currentDatabase(), '`+metricsTable+`', @query, @from, @to, @step)`,
		chgo.Named("query", query), chgo.Named("from", from), chgo.Named("to", to), chgo.Named("step", step))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []rawSeries
	for rows.Next() {
		var names, vals []string
		var s rawSeries
		if err := rows.Scan(&names, &vals, &s.ts, &s.vs); err != nil {
			return nil, err
		}
		s.tags = make(map[string]string, len(names))
		for i, n := range names {
			s.tags[n] = vals[i]
		}
		res = append(res, s)
	}
	return res, rows.Err()
}

// QueryRange evaluates query ($RANGE is three steps) and returns the series with
// the labels filter keeps, NaN points dropped. Series that have the same labels
// after filtering are merged, the last one wins where they overlap.
func (c *Client) QueryRange(ctx context.Context, query string, filter FilterLabelsF, from, to timeseries.Time, step timeseries.Duration) ([]*model.MetricValues, error) {
	query = strings.ReplaceAll(query, "$RANGE", fmt.Sprintf("%ds", int64(step*3)))
	from, to = from.Truncate(step), to.Truncate(step)
	series, err := c.queryRange(ctx, query, int64(from), int64(to), int64(step))
	if err != nil {
		return nil, err
	}
	res := map[uint64]*model.MetricValues{}
	for _, s := range series {
		ls := map[string]string{}
		for name, value := range s.tags {
			if name != promModel.MetricNameLabel && value != "" && filter(name) {
				ls[name] = value
			}
		}
		hash := promModel.LabelsToSignature(ls)
		mv := res[hash]
		for i, t := range s.ts {
			v := float32(s.vs[i])
			if timeseries.IsNaN(v) {
				continue
			}
			if mv == nil {
				mv = &model.MetricValues{Labels: ls, LabelsHash: hash, Values: timeseries.New(from, int(to.Sub(from)/step)+1, step)}
				res[hash] = mv
			}
			mv.Values.Set(timeseries.Time(t), v)
		}
	}
	return maps.Values(res), nil
}

// MetricNames are the metric names that have at least one series.
func (c *Client) MetricNames(ctx context.Context) (map[string]bool, error) {
	rows, err := c.c.Query(ch.TimeSeriesContext(ctx), `SELECT DISTINCT metric_name FROM timeSeriesTags(currentDatabase(), '`+metricsTable+`')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		res[n] = true
	}
	return res, rows.Err()
}

// since is how far back label values and series are looked up.
const since = time.Hour

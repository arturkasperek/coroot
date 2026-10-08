package collector

import (
	"testing"
	"time"

	"github.com/ClickHouse/ch-go"
	chproto "github.com/ClickHouse/ch-go/proto"
	coch "github.com/coroot/coroot/ch"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func series(name string, extra map[string]string, samples ...prompb.Sample) prompb.TimeSeries {
	ls := []prompb.Label{{Name: "__name__", Value: name}}
	for k, v := range extra {
		ls = append(ls, prompb.Label{Name: k, Value: v})
	}
	return prompb.TimeSeries{Labels: ls, Samples: samples}
}

// snapshot copies what a query carries: the batch reuses (resets) its columns
// as soon as exec returns.
type snapshot struct {
	body     string
	settings []ch.Setting
	cols     []string
	names    []string
	tags     []map[string]string
	ts       [][]time.Time
	vs       [][]float64
	strs     [][]string // for the metadata query: one slice per column
}

func record(into *[]snapshot) func(q ch.Query) error {
	return func(q ch.Query) error {
		sn := snapshot{body: q.Body, settings: q.Settings}
		for _, c := range q.Input {
			sn.cols = append(sn.cols, c.Name)
		}
		switch q.Body {
		case insertSamplesSQL:
			name := q.Input[0].Data.(*chproto.ColStr)
			tags := q.Input[1].Data.(*chproto.ColMap[string, string])
			ts := q.Input[2].Data.(*chproto.ColArr[time.Time])
			vs := q.Input[3].Data.(*chproto.ColArr[float64])
			for i := 0; i < name.Rows(); i++ {
				sn.names = append(sn.names, name.Row(i))
				sn.tags = append(sn.tags, tags.Row(i))
				sn.ts = append(sn.ts, append([]time.Time(nil), ts.Row(i)...))
				sn.vs = append(sn.vs, append([]float64(nil), vs.Row(i)...))
			}
		case insertMetadataSQL:
			for _, c := range q.Input {
				col := c.Data.(*chproto.ColStr)
				var vals []string
				for i := 0; i < col.Rows(); i++ {
					vals = append(vals, col.Row(i))
				}
				sn.strs = append(sn.strs, vals)
			}
		}
		*into = append(*into, sn)
		return nil
	}
}

func TestMetricsBatchWritesOneRowPerSeries(t *testing.T) {
	var got []snapshot
	b := NewMetricsBatch(1000, time.Hour, record(&got))
	defer b.Close()
	b.Add(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series("up", map[string]string{"job": "a", "empty": ""}, prompb.Sample{Timestamp: 1_700_000_000_000, Value: 1}, prompb.Sample{Timestamp: 1_700_000_015_500, Value: 0}),
		series("up", map[string]string{"job": "b"}, prompb.Sample{Timestamp: 1_700_000_000_000, Value: 0}),
		series("no_samples", nil),
	}})
	b.save()

	require.Len(t, got, 1)
	q := got[0]
	assert.Equal(t, insertSamplesSQL, q.body)
	assert.Equal(t, coch.TimeSeriesSettings, q.settings)
	assert.Equal(t, []string{"name", "tags", "ts", "vs"}, q.cols)
	require.Equal(t, []string{"up", "up"}, q.names, "one row per series that has samples, not per sample")
	assert.Equal(t, map[string]string{"job": "a"}, q.tags[0], "the empty label is dropped, the metric name is not a label")
	assert.Equal(t, map[string]string{"job": "b"}, q.tags[1])
	require.Len(t, q.ts[0], 2)
	assert.Equal(t, int64(1_700_000_015_500), q.ts[0][1].UnixMilli(), "millisecond precision is kept")
	assert.Equal(t, []float64{1, 0}, q.vs[0])
	assert.Equal(t, []float64{0}, q.vs[1])

	assert.Zero(t, b.rows, "reset for the next batch")
	assert.Zero(t, b.name.Rows())
}

func TestMetricsBatchFlushesAtTheSampleLimit(t *testing.T) {
	var queries []snapshot
	b := NewMetricsBatch(3, time.Hour, record(&queries))
	defer b.Close()
	s := func(n int) []prompb.Sample {
		out := make([]prompb.Sample, n)
		for i := range out {
			out[i] = prompb.Sample{Timestamp: int64(1_700_000_000_000 + i*15_000), Value: 1}
		}
		return out
	}
	b.Add(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{series("up", nil, s(2)...)}})
	assert.Empty(t, queries)
	b.Add(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{series("up", map[string]string{"x": "1"}, s(2)...)}})
	assert.Len(t, queries, 1, "4 samples reached the limit of 3")
}

func TestMetricsBatchWritesMetadata(t *testing.T) {
	var got []snapshot
	b := NewMetricsBatch(1000, time.Hour, record(&got))
	defer b.Close()
	b.Add(&prompb.WriteRequest{Metadata: []prompb.MetricMetadata{
		{MetricFamilyName: "http_requests", Type: prompb.MetricMetadata_COUNTER, Help: "requests", Unit: "1"},
	}})
	b.save()
	require.Len(t, got, 1)
	assert.Equal(t, insertMetadataSQL, got[0].body)
	assert.Equal(t, coch.TimeSeriesSettings, got[0].settings)
	assert.Equal(t, [][]string{{"http_requests"}, {"COUNTER"}, {"1"}, {"requests"}}, got[0].strs)
}

func TestMetricsBatchWithoutDataWritesNothing(t *testing.T) {
	var queries []snapshot
	b := NewMetricsBatch(1000, time.Hour, record(&queries))
	defer b.Close()
	b.save()
	assert.Empty(t, queries)
}

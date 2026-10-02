package collector

import (
	"testing"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/require"
)

func series(name string, extra map[string]string, samples ...prompb.Sample) prompb.TimeSeries {
	ls := []prompb.Label{{Name: "__name__", Value: name}}
	for k, v := range extra {
		ls = append(ls, prompb.Label{Name: k, Value: v})
	}
	return prompb.TimeSeries{Labels: ls, Samples: samples}
}

func TestMetricsBatchWritesSeriesOncePerBatch(t *testing.T) {
	var queries []ch.Query
	b := NewMetricsBatch(1000, time.Hour, func(q ch.Query) error { queries = append(queries, q); return nil })
	defer b.Close()
	b.Add(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series("up", map[string]string{"job": "a"}, prompb.Sample{Timestamp: 1_700_000_000_000, Value: 1}, prompb.Sample{Timestamp: 1_700_000_015_000, Value: 1}),
		series("up", map[string]string{"job": "b"}, prompb.Sample{Timestamp: 1_700_000_000_000, Value: 0}),
	}})
	require.Len(t, b.series, 2, "one entry per series, not per sample")
	require.Equal(t, 3, b.Timestamp.Rows())
	require.Equal(t, 3, b.Value.Rows())

	b.save()
	require.Len(t, queries, 2)
	require.Contains(t, queries[0].Body, "metrics_series", "series go first, so samples always find their labels")
	require.Contains(t, queries[1].Body, "metrics_samples")
	var sampleCols []string
	for _, c := range queries[1].Input {
		sampleCols = append(sampleCols, c.Name)
	}
	require.NotContains(t, sampleCols, "Labels", "samples carry no labels")
	require.Empty(t, b.series, "reset for the next batch")
	require.Zero(t, b.Timestamp.Rows())
}

func TestMetricsBatchSeriesKeepNewestSampleTime(t *testing.T) {
	b := NewMetricsBatch(1000, time.Hour, func(q ch.Query) error { return nil })
	defer b.Close()
	b.Add(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series("up", map[string]string{"job": "a"}, prompb.Sample{Timestamp: 1_700_000_015_000, Value: 1}, prompb.Sample{Timestamp: 1_700_000_000_000, Value: 1}),
	}})
	for _, sr := range b.series {
		require.Equal(t, int64(1_700_000_015), sr.lastSeen.Unix())
		require.Equal(t, "up", sr.name)
		require.Len(t, sr.labels, 1, "the metric name is not a label")
	}
}

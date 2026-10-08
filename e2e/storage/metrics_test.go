//go:build e2e

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/ClickHouse/ch-go"
	coch "github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/collector"
	"github.com/coroot/coroot/db"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ts(name string, lbls map[string]string, samples []prompb.Sample) prompb.TimeSeries {
	ls := []prompb.Label{{Name: "__name__", Value: name}}
	for k, v := range lbls {
		ls = append(ls, prompb.Label{Name: k, Value: v})
	}
	return prompb.TimeSeries{Labels: ls, Samples: samples}
}

// Metrics go in through the real collector (remote write batch) and are found
// again in the TimeSeries table. Reading them back with PromQL is Task 4's test.
func TestMetricsWrittenIntoTimeSeries(t *testing.T) {
	e := chtest.New(t)
	ll := e.LL
	ctx := context.Background()

	end := time.Now().Truncate(time.Minute)
	start := end.Add(-10 * time.Minute)
	samples := func(f func(i int) float64) []prompb.Sample {
		var out []prompb.Sample
		for i := 0; i <= 40; i++ { // every 15 s
			out = append(out, prompb.Sample{Timestamp: start.Add(time.Duration(i) * 15 * time.Second).UnixMilli(), Value: f(i)})
		}
		return out
	}
	all := []prompb.TimeSeries{
		ts("up", map[string]string{"job": "api", "instance": "i1", "namespace": "a", "empty": ""}, samples(func(int) float64 { return 1 })),
		ts("up", map[string]string{"job": "api", "instance": "i2", "namespace": "a"}, samples(func(int) float64 { return 0 })),
		ts("up", map[string]string{"job": "db", "instance": "i3", "namespace": "b"}, samples(func(int) float64 { return 1 })),
		ts("http_requests_total", map[string]string{"job": "api", "instance": "i1", "namespace": "a"}, samples(func(i int) float64 { return float64(i) * 15 })),
		ts("http_requests_total", map[string]string{"job": "api", "instance": "i2", "namespace": "a"}, samples(func(i int) float64 { return float64(i) * 30 })),
	}
	// two batches, each series written twice (first and second half of its samples):
	// the engine must keep one tags row per series
	for part := 0; part < 2; part++ {
		batch := collector.NewMetricsBatch(1_000_000, time.Hour, func(q ch.Query) error { return ll.Do(ctx, q) })
		for _, s := range all {
			half := len(s.Samples) / 2
			if part == 0 {
				s.Samples = s.Samples[:half]
			} else {
				s.Samples = s.Samples[half:]
			}
			batch.Add(&prompb.WriteRequest{
				Timeseries: []prompb.TimeSeries{s},
				Metadata:   []prompb.MetricMetadata{{MetricFamilyName: "up", Type: prompb.MetricMetadata_GAUGE, Help: "is up"}},
			})
		}
		batch.Close()
	}

	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()
	q := func(query string) uint64 {
		t.Helper()
		var n uint64
		require.NoError(t, c.QueryRow(coch.TimeSeriesContext(ctx), query).Scan(&n))
		return n
	}
	// replicated: wait until the node this connection landed on has everything
	require.Eventually(t, func() bool {
		return q(`SELECT count() FROM timeSeriesData(currentDatabase(), 'metrics')`) == 5*41
	}, 20*time.Second, 200*time.Millisecond)

	// tags rows of a series written twice are merged by the engine in the background: count series, not rows
	assert.Equal(t, uint64(5), q(`SELECT uniqExact(id) FROM timeSeriesTags(currentDatabase(), 'metrics')`), "one series each")
	assert.Equal(t, uint64(3), q(`SELECT uniqExact(id) FROM timeSeriesTags(currentDatabase(), 'metrics') WHERE metric_name = 'up'`))
	assert.Zero(t, q(`SELECT count() FROM timeSeriesTags(currentDatabase(), 'metrics') WHERE mapContains(tags, 'empty')`), "an empty label is not stored")
	assert.Equal(t, uint64(41), q(`SELECT count() FROM timeSeriesData(currentDatabase(), 'metrics') WHERE id IN (SELECT id FROM timeSeriesTags(currentDatabase(), 'metrics') WHERE tags['instance'] = 'i3')`))
	assert.Equal(t, uint64(1), q(`SELECT uniqExact(metric_family_name) FROM timeSeriesMetricFamilies(currentDatabase(), 'metrics') WHERE metric_family_name = 'up'`))
}

//go:build e2e

package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/prom"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/require"
)

// Timings of PromQL on the split metrics tables. COROOT_BENCH=1 to run.
func TestBenchMetrics(t *testing.T) {
	if os.Getenv("COROOT_BENCH") == "" {
		t.Skip("COROOT_BENCH is not set")
	}
	e := chtest.New(t)
	ctx := context.Background()
	end := time.Now().Truncate(time.Minute)
	start := end.Add(-6 * time.Hour)
	// 300 metrics; metric_0 has 20k series, the others fewer; a sample every 15 s
	e.Exec(t, `
CREATE TABLE series_src (MetricName LowCardinality(String), MetricHash UInt64, Labels Map(LowCardinality(String), String)) ENGINE MergeTree ORDER BY (MetricName, MetricHash)`)
	e.Exec(t, `
INSERT INTO series_src
SELECT name, cityHash64(name, sn),
  map('instance', concat('10.0.', toString(sn % 40), '.1:80'), 'job', 'node-agent', 'namespace', concat('ns-', toString(sn % 12)),
      'pod', concat('app-', toString(sn % 300), '-', toString(sn % 7)), 'container_id', concat('/k8s/ns-', toString(sn % 12), '/app-', toString(sn % 300), '/c', toString(sn)),
      'destination', concat('10.1.', toString(sn % 256), '.', toString(sn % 13), ':5432'), 'status', ['ok','failed'][1 + sn % 2])
FROM (SELECT concat('metric_', toString(number)) AS name, greatest(5, intDiv(20000, number + 1)) AS n FROM numbers(300))
ARRAY JOIN range(n) AS sn`)
	e.Exec(t, `
INSERT INTO metrics_series SELECT MetricName, MetricHash, Labels, now() FROM series_src`)
	e.Exec(t, "INSERT INTO metrics_samples SELECT MetricName, MetricHash, toDateTime('"+start.UTC().Format("2006-01-02 15:04:05")+"', 'UTC') + t * 15, (t * (MetricHash % 100)) + (cityHash64(MetricHash, t) % 1000) / 1000.0 FROM series_src ARRAY JOIN range(1440) AS t")
	e.Exec(t, "OPTIMIZE TABLE metrics_samples FINAL")
	e.Exec(t, "OPTIMIZE TABLE metrics_series FINAL")

	client, err := prom.NewClient(&db.IntegrationPrometheus{UseClickHouse: true, RefreshInterval: 15}, e.Config)
	require.NoError(t, err)
	defer client.Close()
	run := func(name, query string, hours int) {
		to := timeseries.TimeFromStandard(end)
		from := timeseries.TimeFromStandard(end.Add(-time.Duration(hours) * time.Hour))
		var best time.Duration
		var n int
		for i := 0; i < 3; i++ {
			s := time.Now()
			res, err := client.QueryRange(ctx, query, prom.FilterLabelsKeepAll, from, to, 60)
			require.NoError(t, err, query)
			n = len(res)
			if d := time.Since(s); i == 0 || d < best {
				best = d
			}
		}
		t.Logf("BENCH %-52s %8.1f ms  (%d series returned)", name, float64(best.Microseconds())/1000, n)
	}
	var _ = model.MetricValues{}
	run("metric with 20k series, 1 h", `metric_0`, 1)
	run("same, 6 h", `metric_0`, 6)
	run("namespace= matcher, 1 h", `metric_0{namespace="ns-3"}`, 1)
	run("namespace=~ matcher, 1 h", `metric_0{namespace=~"ns-(1|2)"}`, 1)
	run("one container, 1 h", `metric_5{container_id="/k8s/ns-7/app-7/c7"}`, 1)
	run("small metric, 6 h", `metric_200`, 6)
	run("sum by (namespace) (rate(...[5m])), 1 h", `sum by (namespace) (rate(metric_0[5m]))`, 1)
}

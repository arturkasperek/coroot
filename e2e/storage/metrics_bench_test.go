//go:build e2e

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	coch "github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/promql"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/world"
	"github.com/stretchr/testify/require"
)

// Timings of the two read paths: PromQL on the TimeSeries table (what the
// evaluator runs every cycle) and world.Store (what the pages run).
// COROOT_BENCH=1 to run.
func TestBenchMetrics(t *testing.T) {
	if os.Getenv("COROOT_BENCH") == "" {
		t.Skip("COROOT_BENCH is not set")
	}
	e := chtest.New(t)
	ctx := context.Background()
	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-6 * time.Hour)
	const samples = 1440 // 6 h every 15 s

	// 300 metrics; metric_0 has 20k series, the others fewer
	require.NoError(t, e.LL.ExecWithSettings(ctx, fmt.Sprintf(`
INSERT INTO metrics (metric_name, tags, samples)
SELECT name,
  map('instance', concat('10.0.', toString(sn %% 40), '.1:80'), 'job', 'node-agent', 'namespace', concat('ns-', toString(sn %% 12)),
      'pod', concat('app-', toString(sn %% 300), '-', toString(sn %% 7)), 'container_id', concat('/k8s/ns-', toString(sn %% 12), '/app-', toString(sn %% 300), '/c', toString(sn)),
      'destination', concat('10.1.', toString(sn %% 256), '.', toString(sn %% 13), ':5432'), 'status', ['ok','failed'][1 + sn %% 2]),
  arrayMap(i -> (toDateTime64(%d, 3) + toIntervalSecond(i*15), toFloat64(i * (sn %% 100) + (cityHash64(sn, i) %% 1000) / 1000.0)), range(%d))
FROM (SELECT concat('metric_', toString(number)) AS name, greatest(5, intDiv(20000, number + 1)) AS n FROM numbers(300))
ARRAY JOIN range(n) AS sn`, start.Unix(), samples), coch.TimeSeriesSettings))

	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	client := promql.New(c, 15*timeseries.Second)
	to := timeseries.TimeFromStandard(end)

	timed := func(name string, f func() (int, error)) {
		var best time.Duration
		var n int
		for i := 0; i < 3; i++ {
			s := time.Now()
			var err error
			n, err = f()
			require.NoError(t, err, name)
			if d := time.Since(s); i == 0 || d < best {
				best = d
			}
		}
		t.Logf("%-55s %6d series  %v", name, n, best.Round(time.Millisecond))
	}

	require.Eventually(t, func() bool {
		res, err := client.QueryRange(ctx, `metric_100`, promql.FilterLabelsKeepAll, to.Add(-timeseries.Hour), to, 15)
		return err == nil && len(res) > 0
	}, 2*time.Minute, time.Second)

	for _, q := range []struct{ name, query string }{
		{"selector, 200 series", `metric_100`},
		{"rate, 200 series", `rate(metric_100[1m])`},
		{"sum by (namespace) (rate), 20k series", `sum by (namespace) (rate(metric_0[1m]))`},
		{"selector with matcher, 20k series", `metric_0{status="failed"}`},
	} {
		for _, hours := range []int{1, 6} {
			from := to.Add(-timeseries.Duration(hours) * timeseries.Hour)
			timed(fmt.Sprintf("promql %s (%d h)", q.name, hours), func() (int, error) {
				res, err := client.QueryRange(ctx, q.query, promql.FilterLabelsKeepAll, from, to, 15)
				return len(res), err
			})
		}
	}

	// world store: 50 queries x 100 series, a day of 15 s points
	const query = `rate(x[$RANGE])`
	worldInsert(t, e, fmt.Sprintf(`
INSERT INTO world_series_distributed
SELECT concat('%s', toString(q)), s, map('app', concat('a', toString(s))), now() FROM (SELECT number AS q FROM numbers(50)) ARRAY JOIN range(100) AS s`, query))
	worldInsert(t, e, fmt.Sprintf(`
INSERT INTO world_points_distributed
SELECT concat('%s', toString(q)), s, toDateTime(%d) + t * 15, toFloat64(t %% 100 + s)
FROM (SELECT number AS q FROM numbers(50)) ARRAY JOIN range(100) AS s ARRAY JOIN range(5760) AS t`, query, end.Add(-24*time.Hour).Unix()))
	for _, r := range []struct {
		name  string
		hours int
		step  timeseries.Duration
	}{
		{"1 h, step 15 s", 1, 15},
		{"24 h, step 5 min", 24, 5 * timeseries.Minute},
		{"24 h, step 1 h", 24, timeseries.Hour},
	} {
		from := to.Add(-timeseries.Duration(r.hours) * timeseries.Hour)
		timed("world store "+r.name, func() (int, error) {
			res, err := world.NewStore(c).QueryRange(ctx, query+"7", from, to, r.step, timeseries.FillAvg)
			return len(res), err
		})
	}
}

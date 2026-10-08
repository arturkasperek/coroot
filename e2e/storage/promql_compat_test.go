//go:build e2e

package storage

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Coroot's metrics are read with ClickHouse's own PromQL (prometheusQueryRange
// over a TimeSeries table, a private preview in 26.9). This test pins every
// behaviour the rest of the code relies on, so a ClickHouse upgrade that changes
// one of them fails here and not on a chart.

var compatT0 = time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC) // aligned to 15 s; samples every 15 s for 10 min

const compatTable = `CREATE TABLE compat @on_metrics_cluster ENGINE = TimeSeries
SETTINGS recent_samples_ttl_seconds = 0
SAMPLES INNER ENGINE = ReplicatedMergeTree('/clickhouse/tables/compat/{database}/samples', '{replica}') ORDER BY (id, timestamp)
TAGS INNER ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/compat/{database}/tags', '{replica}') PRIMARY KEY metric_name ORDER BY (metric_name, id)
METRIC FAMILIES INNER ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/compat/{database}/families', '{replica}') ORDER BY metric_family_name`

func compatInsert(name string, tags map[string]string, n int, value string) string {
	var kv []string
	for k, v := range tags {
		kv = append(kv, fmt.Sprintf("'%s','%s'", k, v))
	}
	return fmt.Sprintf(`INSERT INTO compat (metric_name, tags, samples) SELECT '%s', map(%s),
  arrayMap(i -> (toDateTime64('2026-10-05 08:00:00', 3) + toIntervalSecond(i*15), toFloat64(%s)), range(%d))`,
		name, strings.Join(kv, ","), value, n)
}

type compatPoint struct {
	t time.Time
	v float64
}

type compatEnvT struct {
	c *clickhouse.Client
}

func compatEnv(t *testing.T) *compatEnvT {
	e := chtest.New(t)
	ctx := context.Background()
	require.NoError(t, e.LL.ExecWithSettings(ctx, compatTable, ch.TimeSeriesSettings))
	ins := func(q string) { require.NoError(t, e.LL.ExecWithSettings(ctx, q, ch.TimeSeriesSettings), q) }
	// counter c{app=a}: +15 per sample (rate 1/s); gauge g{app=a, empty=""} = 5 (the
	// empty label is dropped by the collector, here it is written to see what PromQL does);
	// stop{app=b}: only the first 2 minutes (8 samples)
	ins(compatInsert("c", map[string]string{"app": "a"}, 40, "i*15"))
	ins(compatInsert("g", map[string]string{"app": "a"}, 40, "5"))
	ins(compatInsert("stop", map[string]string{"app": "b"}, 8, "1"))
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return &compatEnvT{c: c}
}

// waitSeries waits until the node the connection landed on has all n series:
// the table is replicated and an insert on another node is not instant.
func (e *compatEnvT) waitSeries(t *testing.T, n uint64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var got uint64
		require.NoError(t, e.c.QueryRow(ch.TimeSeriesContext(context.Background()),
			`SELECT count() FROM timeSeriesTags(currentDatabase(), 'compat')`).Scan(&got))
		if got == n {
			// the samples are replicated separately from the tags
			var samples uint64
			require.NoError(t, e.c.QueryRow(ch.TimeSeriesContext(context.Background()),
				`SELECT count() FROM timeSeriesData(currentDatabase(), 'compat')`).Scan(&samples))
			if samples == 40+40+8 {
				return
			}
		}
		require.True(t, time.Now().Before(deadline), "replication: %d series", got)
		time.Sleep(200 * time.Millisecond)
	}
}

// queryRange runs PromQL over [from, to] at 15 s. Series are keyed "name=value,..."
// with the labels sorted, __name__ included.
func (e *compatEnvT) queryRange(t *testing.T, q string, from, to time.Time) map[string][]compatPoint {
	t.Helper()
	out := map[string][]compatPoint{}
	rows, err := e.c.Query(ch.TimeSeriesContext(context.Background()),
		`SELECT arrayMap(x -> concat(x.1, '=', x.2), tags) AS tags,
		        arrayMap(p -> p.1, samples) AS ts, arrayMap(p -> p.2, samples) AS vs
		 FROM prometheusQueryRange(currentDatabase(), 'compat', @q, @from, @to, 15)`,
		chgo.Named("q", q), chgo.Named("from", from.Unix()), chgo.Named("to", to.Unix()))
	require.NoError(t, err, q)
	defer rows.Close()
	for rows.Next() {
		var tags []string
		var ts []time.Time
		var vs []float64
		require.NoError(t, rows.Scan(&tags, &ts, &vs))
		sort.Strings(tags)
		k := strings.Join(tags, ",")
		for i := range ts {
			out[k] = append(out[k], compatPoint{ts[i], vs[i]})
		}
	}
	require.NoError(t, rows.Err())
	return out
}

func TestPromQLCompat(t *testing.T) {
	e := compatEnv(t)
	e.waitSeries(t, 3)
	from, to := compatT0.Add(time.Minute), compatT0.Add(5*time.Minute)

	t.Run("rate over 45s", func(t *testing.T) {
		res := e.queryRange(t, `rate(c[45s])`, from, to)
		require.Len(t, res, 1)
		for _, p := range res["app=a"] {
			assert.InDelta(t, 1.0, p.v, 1e-9, p.t)
		}
	})
	t.Run("increase is implemented", func(t *testing.T) {
		res := e.queryRange(t, `increase(c[45s])`, from, to)
		require.Len(t, res, 1)
	})
	t.Run("an empty label is an absent label", func(t *testing.T) {
		assert.Len(t, e.queryRange(t, `g{missing=""}`, from, to), 1)
		assert.Empty(t, e.queryRange(t, `g{app=""}`, from, to))
		assert.Len(t, e.queryRange(t, `g{app!=""}`, from, to), 1)
	})
	t.Run("sum by, or, binary operators, comparison", func(t *testing.T) {
		// `or` compares label sets without __name__: g{app=a} has the labels of sum by (app) (c)
		assert.Len(t, e.queryRange(t, `sum by (app) (c) or g`, from, to), 1)
		assert.Len(t, e.queryRange(t, `c or stop`, from, to), 2)
		assert.Len(t, e.queryRange(t, `c * 2`, from, to), 1)
		assert.Len(t, e.queryRange(t, `g > 0`, from, to), 1)
		assert.Empty(t, e.queryRange(t, `g > 10`, from, to))
		res := e.queryRange(t, `c % 10000000`, from, to)
		require.Len(t, res, 1)
	})
	t.Run("regex matchers are anchored", func(t *testing.T) {
		assert.Len(t, e.queryRange(t, `c{app=~"a"}`, from, to), 1)
		assert.Empty(t, e.queryRange(t, `c{app=~"a."}`, from, to), "a. must not match a")
		assert.Len(t, e.queryRange(t, `{__name__=~"c|g"}`, from, to), 2)
	})
	t.Run("a series that stops yields NaN or nothing, never a value", func(t *testing.T) {
		// the last sample of stop is at t0+105s; past the 5 min lookback Prometheus has no point
		res := e.queryRange(t, `stop`, compatT0.Add(time.Minute), compatT0.Add(10*time.Minute))
		for _, p := range res["__name__=stop,app=b"] {
			if p.t.After(compatT0.Add(105*time.Second + 5*time.Minute)) {
				assert.True(t, math.IsNaN(p.v), "stale point %v", p)
			}
		}
	})
	t.Run("one query, many steps", func(t *testing.T) {
		res := e.queryRange(t, `c`, from, to)
		assert.Len(t, res["__name__=c,app=a"], 17) // 1m..5m inclusive at 15 s
	})
	t.Run("time() replaces timestamp()", func(t *testing.T) {
		res := e.queryRange(t, `time() - g`, from, to)
		require.Len(t, res, 1)
		p := res["app=a"][0]
		assert.InDelta(t, float64(p.t.Unix())-5, p.v, 1e-9)
	})
	t.Run("histogram_quantile and over_time functions", func(t *testing.T) {
		assert.NotEmpty(t, e.queryRange(t, `max_over_time(g[1m])`, from, to))
		assert.NotEmpty(t, e.queryRange(t, `sum(rate(c[1m]))`, from, to))
	})
	t.Run("every node of the metrics cluster holds every series", func(t *testing.T) {
		// the remote nodes do not inherit the database of the connection
		var database string
		require.NoError(t, e.c.QueryRow(context.Background(), `SELECT currentDatabase()`).Scan(&database))
		var nodes, minSeries uint64
		require.NoError(t, e.c.QueryRow(ch.TimeSeriesContext(context.Background()), `SELECT count(), min(n) FROM
			clusterAllReplicas('`+ch.MetricsCluster+`', view(SELECT count() AS n FROM timeSeriesTags('`+database+`', 'compat')))`).Scan(&nodes, &minSeries))
		assert.Equal(t, uint64(2), nodes)
		assert.Equal(t, uint64(3), minSeries)
	})
}

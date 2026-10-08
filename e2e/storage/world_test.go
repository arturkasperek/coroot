//go:build e2e

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	chgo "github.com/ClickHouse/ch-go"
	coch "github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/collector"
	"github.com/coroot/coroot/constructor"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/promql"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/world"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// foreground: the insert returns when the shard has the rows (and the rollups,
// which are materialized views on the shard, have them too)
var foreground = []chgo.Setting{chgo.SettingInt("distributed_foreground_insert", 1)}

func worldInsert(t *testing.T, e *chtest.Env, query string) {
	t.Helper()
	require.NoError(t, e.LL.ExecWithSettings(context.Background(), query, foreground), query)
}

// The store must give, at every page step, exactly what today's chunk cache
// gives: 15 s points widened with the query's FillFunc. The expected series are
// made by running timeseries.FillAny / FillAvg / FillSum / ... on the same points.
func TestWorldStoreMatchesFillFuncs(t *testing.T) {
	e := chtest.New(t)
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	const query = `rate(x[$RANGE])`
	t0 := timeseries.TimeFromStandard(time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour))
	const n = 720 // 3 h of 15 s points: value i at t0 + 15 i, with a 7.5 min gap
	vals := make([]float32, n)
	for i := range vals {
		vals[i] = float32(i)
		if i >= 100 && i < 130 {
			vals[i] = timeseries.NaN
		}
	}
	worldInsert(t, e, fmt.Sprintf(`INSERT INTO world_series_distributed VALUES ('%s', 1, map('app','a','container_id','/k8s/ns/p/c','machine_id','m1'), now())`, query))
	var rows []string
	for i, v := range vals {
		if !timeseries.IsNaN(v) {
			rows = append(rows, fmt.Sprintf("('%s', 1, toDateTime(%d), %g)", query, t0.Add(timeseries.Duration(i)*15), v))
		}
	}
	worldInsert(t, e, "INSERT INTO world_points_distributed VALUES "+strings.Join(rows, ","))

	s := world.NewStore(c)
	ctx := context.Background()
	from, to := t0.Add(timeseries.Hour), t0.Add(3*timeseries.Hour)
	fills := map[string]timeseries.FillFunc{"any": timeseries.FillAny, "avg": timeseries.FillAvg, "sum": timeseries.FillSum, "max": timeseries.FillMax}
	for _, step := range []timeseries.Duration{15, 2 * timeseries.Minute, 5 * timeseries.Minute, 15 * timeseries.Minute, timeseries.Hour} {
		for name, fill := range fills {
			f, tt := from.Truncate(step), to.Truncate(step)
			want := timeseries.New(f, int(tt.Sub(f)/step)+1, step)
			fill(want, t0, 15, vals)

			var got []byte
			require.Eventually(t, func() bool {
				res, err := s.QueryRange(ctx, query, from, to, step, fill)
				require.NoError(t, err)
				if len(res) != 1 {
					return false
				}
				assert.Equal(t, map[string]string{"app": "a"}, map[string]string(res[0].Labels))
				assert.Equal(t, "/k8s/ns/p/c", res[0].ContainerId, "the labels the constructor reads from fields are not in Labels")
				assert.Equal(t, "m1", res[0].MachineID)
				got, _ = res[0].Values.MarshalJSON()
				return true
			}, 15*time.Second, 300*time.Millisecond, "step %d, %s", step, name)
			wantJSON, _ := want.MarshalJSON()
			if step < 5*timeseries.Minute {
				assert.JSONEq(t, string(wantJSON), string(got), "step %d, %s", step, name)
				continue
			}
			// A rollup holds whole buckets, so its first point is the whole bucket that ends
			// at from; the chunk cache gave the part of it that lies after from (just the
			// sample at from). The first point of a chart is an artifact either way: compare
			// the rest.
			assert.Equal(t, pointsAfterFirst(t, wantJSON), pointsAfterFirst(t, got), "step %d, %s", step, name)
		}
	}

	t.Run("LastEvaluated is the newest point", func(t *testing.T) {
		last, err := s.LastEvaluated(ctx)
		require.NoError(t, err)
		assert.Equal(t, t0.Add(timeseries.Duration(n-1)*15), last)
	})
	t.Run("an unknown query has no series", func(t *testing.T) {
		res, err := s.QueryRange(ctx, "nope", from, to, 15, timeseries.FillAny)
		require.NoError(t, err)
		assert.Empty(t, res)
	})
}

func pointsAfterFirst(t *testing.T, tsJSON []byte) []any {
	t.Helper()
	var v []any
	require.NoError(t, json.Unmarshal(tsJSON, &v), string(tsJSON))
	require.NotEmpty(t, v)
	return v[1:]
}

// The evaluator reads the metrics table with PromQL and stores the results.
func TestWorldEvaluator(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	project := &db.Project{Id: "evalproject", Name: "test", Settings: db.ProjectSettings{Integrations: db.Integrations{Clickhouse: e.Config}}}

	cc, err := clickhouse.NewClient(e.Config, project)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })

	now := timeseries.Now().Truncate(15)
	start := now.Add(-10 * timeseries.Minute)
	put := func(name string, labels map[string]string, from timeseries.Time, n int, value func(i int) float64) {
		ls := []prompb.Label{{Name: "__name__", Value: name}}
		for k, v := range labels {
			ls = append(ls, prompb.Label{Name: k, Value: v})
		}
		var samples []prompb.Sample
		for i := 0; i < n; i++ {
			samples = append(samples, prompb.Sample{Timestamp: int64(from.Add(timeseries.Duration(i)*15)) * 1000, Value: value(i)})
		}
		b := collector.NewMetricsBatch(1_000_000, time.Hour, func(q chgo.Query) error { return e.LL.Do(ctx, q) })
		b.Add(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{{Labels: ls, Samples: samples}}})
		b.Close()
	}
	n := int(now.Sub(start)/15) + 1
	put("up", map[string]string{"job": "api", "instance": "i1", "namespace": "a"}, start, n, func(int) float64 { return 1 })
	put("up", map[string]string{"job": "api", "instance": "i2", "namespace": "a"}, start, n, func(int) float64 { return 0 })
	put("up", map[string]string{"job": "db", "instance": "i3", "namespace": "b"}, start, n, func(int) float64 { return 1 })

	q := func(query string) uint64 {
		t.Helper()
		var v uint64
		require.NoError(t, cc.QueryRow(coch.TimeSeriesContext(ctx), query).Scan(&v))
		return v
	}
	database, err := db.NewSqlite(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.Migrate())
	ev := world.NewEvaluator(database, nil, world.Period, world.Backfill)
	t.Cleanup(ev.Close)
	require.Eventually(t, func() bool {
		names, err := promql.New(cc, 15).MetricNames(ctx)
		return err == nil && names["up"]
	}, 20*time.Second, 300*time.Millisecond)
	// a replica may not have all the samples yet: the first cycle waits for them
	require.Eventually(t, func() bool {
		return q(`SELECT count() FROM timeSeriesData(currentDatabase(), 'metrics')`) == uint64(3*n)
	}, 20*time.Second, 300*time.Millisecond)

	res, err := ev.EvaluateProject(ctx, project, now.Add(30))
	require.NoError(t, err)
	t.Logf("%+v", res)
	assert.Equal(t, 1, res.Queries, "only the queries whose metrics exist: up")
	assert.Greater(t, res.Skipped, 300, "most of the 393 queries read metrics that nobody sends")
	assert.Equal(t, 3, res.Series)
	assert.Equal(t, 3*n, res.Points, "every sample up to now (now+30 minus two steps)")

	count := func(query string) uint64 {
		return q(`SELECT count() FROM @@table_world_points@@ WHERE Query = '` + query + `'`)
	}
	assert.Equal(t, uint64(res.Points), count("up"))
	assert.Equal(t, uint64(3), q(`SELECT uniqExact(SeriesHash) FROM @@table_world_points@@ WHERE Query = 'up'`))
	// the labels are the ones the query declares
	assert.Equal(t, uint64(0), q(`SELECT countIf(mapContains(Labels, 'namespace')) FROM @@table_world_series@@ FINAL WHERE Query = 'up'`))
	assert.Equal(t, uint64(3), q(`SELECT countIf(mapContains(Labels, 'job') AND mapContains(Labels, 'instance')) FROM @@table_world_series@@ FINAL WHERE Query = 'up'`))

	t.Run("a second cycle adds only the new steps and never a duplicate", func(t *testing.T) {
		res2, err := ev.EvaluateProject(ctx, project, now.Add(60))
		require.NoError(t, err)
		assert.Equal(t, 3*2, res2.Points)
		assert.Zero(t, res2.Series, "the labels of a series are written once")
		assert.Equal(t, q(`SELECT count() FROM @@table_world_points@@ WHERE Query = 'up'`),
			q(`SELECT uniqExact(SeriesHash, Timestamp) FROM @@table_world_points@@ WHERE Query = 'up'`))
	})

	t.Run("an evaluator that restarts continues where the data is", func(t *testing.T) {
		before := count("up")
		ev2 := world.NewEvaluator(database, nil, world.Period, world.Backfill)
		t.Cleanup(ev2.Close)
		res3, err := ev2.EvaluateProject(ctx, project, now.Add(60))
		require.NoError(t, err)
		assert.Zero(t, res3.Queries, "everything up to two steps before now is stored")
		assert.Equal(t, before, count("up"))
	})

	t.Run("a metric that appears later is evaluated from its first sample", func(t *testing.T) {
		put("container_restarts_total", map[string]string{"job": "node", "instance": "n1", "container_id": "/k8s/ns/p/c"}, start.Add(timeseries.Minute), n-4, func(i int) float64 { return float64(i / 10) })
		require.Eventually(t, func() bool {
			names, err := promql.New(cc, 15).MetricNames(ctx)
			return err == nil && names["container_restarts_total"]
		}, 20*time.Second, 300*time.Millisecond)
		require.Eventually(t, func() bool {
			return q(`SELECT count() FROM timeSeriesData(currentDatabase(), 'metrics') WHERE id IN (SELECT id FROM timeSeriesTags(currentDatabase(), 'metrics') WHERE metric_name = 'container_restarts_total')`) == uint64(n-4)
		}, 20*time.Second, 300*time.Millisecond)
		res4, err := ev.EvaluateProject(ctx, project, now.Add(60))
		require.NoError(t, err)
		assert.Equal(t, 1, res4.Queries, "only the new query; up is up to date")
		first := q(`SELECT toUInt64(toUInt32(min(Timestamp))) FROM @@table_world_points@@ WHERE Query = 'container_restarts_total % 10000000'`)
		assert.Equal(t, uint64(start.Add(timeseries.Minute)), first, "from the first sample, not from now")
	})
}

// A recording rule is a function of the World built from the stored results; its
// output is stored like a query and read back like one.
func TestWorldRecordingRules(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	project := &db.Project{Id: "ruleproject", Name: "test", Settings: db.ProjectSettings{Integrations: db.Integrations{Clickhouse: e.Config}}}
	cc, err := clickhouse.NewClient(e.Config, project)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	database, err := db.NewSqlite(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, database.Migrate())

	var windows [][2]timeseries.Time
	constructor.RecordingRules["rr_e2e_test"] = func(_ *db.DB, _ *db.Project, w *model.World) []*model.MetricValues {
		windows = append(windows, [2]timeseries.Time{w.Ctx.From, w.Ctx.To})
		ts := timeseries.New(w.Ctx.From, int(w.Ctx.To.Sub(w.Ctx.From)/w.Ctx.Step)+1, w.Ctx.Step)
		it := ts.Iter()
		for it.Next() {
			t, _ := it.Value()
			ts.Set(t, float32(t%1000))
		}
		return []*model.MetricValues{{Labels: model.Labels{"application": "a"}, LabelsHash: model.Labels{"application": "a"}.Hash(), Values: ts}}
	}
	t.Cleanup(func() { delete(constructor.RecordingRules, "rr_e2e_test") })

	ev := world.NewEvaluator(database, nil, world.Period, 2*timeseries.Hour)
	t.Cleanup(ev.Close)
	now := timeseries.Now().Truncate(15)
	q := func(query string) uint64 {
		t.Helper()
		var v uint64
		require.NoError(t, cc.QueryRow(ctx, query).Scan(&v))
		return v
	}

	res, err := ev.EvaluateProject(ctx, project, now)
	require.NoError(t, err)
	evaluatedTo := now.Add(-30) // the queries are evaluated up to here
	ruleTo := evaluatedTo.Add(-2 * 15)
	assert.Equal(t, ruleTo, res.To, "a page ends where the rules end: a rule is computed two steps behind the queries")
	// two hours of backfill, built one hour at a time; every World reaches back 10 minutes
	// before its chunk and two steps beyond it
	history := 10 * timeseries.Minute
	first := ruleTo.Add(-2 * timeseries.Hour).Truncate(15)
	require.Len(t, windows, 3, "the backfill is split in World-sized chunks: %v", windows)
	for i, w := range windows {
		chunkFrom := first.Add(timeseries.Duration(i) * timeseries.Hour)
		chunkTo := chunkFrom.Add(timeseries.Hour - 15)
		if chunkTo > ruleTo {
			chunkTo = ruleTo
		}
		wantTo := chunkTo.Add(2 * 15)
		if wantTo > evaluatedTo {
			wantTo = evaluatedTo
		}
		assert.Equal(t, [2]timeseries.Time{chunkFrom.Add(-history), wantTo}, w, "chunk %d", i)
	}
	points := q(`SELECT count() FROM @@table_world_points@@ WHERE Query = 'rr_e2e_test'`)
	assert.Equal(t, uint64(2*240+1), points, "every step of the backfill, once")
	assert.Equal(t, points, uint64(res.Points))
	assert.Equal(t, uint64(1), q(`SELECT count() FROM @@table_world_series@@ FINAL WHERE Query = 'rr_e2e_test'`))
	last, err := world.NewStore(cc).LastEvaluated(ctx)
	require.NoError(t, err)
	assert.Equal(t, ruleTo, last, "the store says where the rules end")

	// the next cycle: only the new steps
	windows = nil
	res, err = ev.EvaluateProject(ctx, project, now.Add(30))
	require.NoError(t, err)
	require.Len(t, windows, 1)
	assert.Equal(t, [2]timeseries.Time{ruleTo.Add(15).Add(-history), now}, windows[0])
	assert.Equal(t, points+2, q(`SELECT count() FROM @@table_world_points@@ WHERE Query = 'rr_e2e_test'`))
	assert.Equal(t, q(`SELECT count() FROM @@table_world_points@@ WHERE Query = 'rr_e2e_test'`),
		q(`SELECT uniqExact(SeriesHash, Timestamp) FROM @@table_world_points@@ WHERE Query = 'rr_e2e_test'`))
	ruleTo2 := now.Add(-30)

	t.Run("a restarted evaluator does not compute or write the rules again", func(t *testing.T) {
		ev2 := world.NewEvaluator(database, nil, world.Period, 2*timeseries.Hour)
		t.Cleanup(ev2.Close)
		windows = nil
		_, err := ev2.EvaluateProject(ctx, project, now.Add(30))
		require.NoError(t, err)
		assert.Empty(t, windows, "the marker tells how far the rules were computed, even for the rules that left no rows")
		assert.Equal(t, points+2, q(`SELECT count() FROM @@table_world_points@@ WHERE Query = 'rr_e2e_test'`))
	})

	t.Run("a rule that is added later is backfilled and the others are not written twice", func(t *testing.T) {
		constructor.RecordingRules["rr_e2e_test2"] = func(_ *db.DB, _ *db.Project, w *model.World) []*model.MetricValues {
			ts := timeseries.New(w.Ctx.From, int(w.Ctx.To.Sub(w.Ctx.From)/w.Ctx.Step)+1, w.Ctx.Step)
			it := ts.Iter()
			for it.Next() {
				t, _ := it.Value()
				ts.Set(t, 1)
			}
			return []*model.MetricValues{{Labels: model.Labels{"application": "b"}, LabelsHash: model.Labels{"application": "b"}.Hash(), Values: ts}}
		}
		t.Cleanup(func() { delete(constructor.RecordingRules, "rr_e2e_test2") })
		ev3 := world.NewEvaluator(database, nil, world.Period, 2*timeseries.Hour)
		t.Cleanup(ev3.Close)
		before := q(`SELECT count() FROM @@table_world_points@@ WHERE Query = 'rr_e2e_test'`)
		_, err := ev3.EvaluateProject(ctx, project, now.Add(30))
		require.NoError(t, err)
		assert.Equal(t, before, q(`SELECT count() FROM @@table_world_points@@ WHERE Query = 'rr_e2e_test'`), "the rule that has rows is not written again")
		assert.Equal(t, uint64(2*240+1), q(`SELECT count() FROM @@table_world_points@@ WHERE Query = 'rr_e2e_test2'`), "the new rule has the whole backfill")
	})

	// read back through the store, with the labels of the rule
	got, err := world.NewStore(cc).QueryRange(ctx, "rr_e2e_test", now.Add(-5*timeseries.Minute), now, 15, timeseries.FillAny)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Labels["application"])
	at, v := got[0].Values.LastNotNull()
	assert.Equal(t, ruleTo2, at)
	assert.Equal(t, float32(ruleTo2%1000), v)
}

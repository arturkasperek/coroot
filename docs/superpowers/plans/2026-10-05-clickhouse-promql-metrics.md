# Metrics on ClickHouse PromQL Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store and query all metrics with ClickHouse's own TimeSeries engine and PromQL, delete Coroot's metrics cache, its embedded Prometheus engine and the external Prometheus integration, and keep pages fast with a precomputed "world" table.

**Architecture:** Agents keep sending Prometheus remote write to the collector, which inserts into one ClickHouse `TimeSeries` table. A small evaluator runs every 30 s: it evaluates each constructor query in ClickHouse (`prometheusQueryRange`) only over the steps not evaluated yet, and appends the results to `world_points`, with 5-minute and 1-hour rollups maintained by materialized views. Pages build the World from `world_points` (one read, no PromQL at page time); dashboards, custom panels and the Prometheus-compatible API run PromQL directly in ClickHouse.

**Tech Stack:** Go 1.25, ClickHouse 26.9.10.4 (`TimeSeries` engine, `prometheusQuery` / `prometheusQueryRange`, settings `enable_time_series_table` and `enable_time_series_aggregate_functions`), ch-go v0.62, clickhouse-go v2, Prometheus `promql/parser` (parsing only, no engine).

**Spec:** this document, section "Design and evidence" (measurements made on the dev cluster on 2026-10-05; scratch code in `$CLAUDE_JOB_DIR/tmp/audit-world/`).

## Global Constraints

- Coroot is deployed only on Kubernetes (Task 12 removes Docker Compose, Docker Swarm and the systemd installer). ClickHouse image is exactly `clickhouse/clickhouse-server:26.9.10.4` (Keeper: `clickhouse/clickhouse-keeper:26.9.10.4`) in `deploy/kind/` and the Kubernetes docs. The TimeSeries engine is a private preview that "may change in backwards-incompatible ways"; the version is pinned and only bumped together with Task 1's compatibility suite passing.
- Every query that touches the TimeSeries table or `prometheusQuery*` sends the settings `enable_time_series_table=1` and `enable_time_series_aggregate_functions=1` (per query, never relying on server profiles).
- No backwards compatibility: no migration of `metrics_series` / `metrics_samples` / `metrics_metadata` data, no support for an external Prometheus, no disk cache.
- ClickHouse is always a cluster (D1). Every new table is created `ON CLUSTER @cluster` with a `Replicated*` engine and gets a `Distributed` table; every test runs against the 2-shard dev cluster.
- Evaluation step is 15 s (the agents' scrape interval); `$RANGE` in a query becomes `45s` (3 × step), exactly as `prom/clickhouse.go:68` does today.
- Evaluator cycle 30 s; backfill window on an empty world table 24 h.
- PromQL semantics are ClickHouse's. Where it differs from Prometheus (stale NaN points, empty labels), Coroot adapts its side (drop NaN, treat empty label as absent).
- Tests that need ClickHouse carry the `e2e` build tag, live in `e2e/` or `e2e/storage/`, and run with `make test-e2e` (see `ch/chtest`). Unit tests stay in their package and need no ClickHouse.
- Never commit from an agent session unless the human asks; the steps below say "Commit" for the human's convenience.

## Design and evidence

### What exists today (and goes away)

| piece | files | role |
|---|---|---|
| metrics tables | `ch/client.go` (`metrics_samples`, `metrics_series`, `metrics_metadata`) | storage written by `collector/metrics.go` |
| Prometheus engine over ClickHouse | `prom/clickhouse.go`, `prom/clickhouse_querier.go` | PromQL evaluated in Go |
| external Prometheus client | `prom/http.go`, `prom/client.go` (`httpClientConfig`) | reading a user's Prometheus |
| metrics cache | `cache/` (~2.1k lines: chunks on disk, SQLite state, compaction, GC, updater, recording rules) | 393 queries every 15 s, World input for pages |
| Prometheus integration | `db.IntegrationPrometheus`, `api/forms`, `front/src/views/IntegrationPrometheus.vue` | configuration UI |

The constructor only needs this interface (`constructor/constructor.go:35`):

```go
type Cache interface {
	QueryRange(ctx context.Context, query string, from, to timeseries.Time, step timeseries.Duration, fillFunc timeseries.FillFunc) ([]*model.MetricValues, error)
	GetStep(from, to timeseries.Time) (timeseries.Duration, error)
}
```

so the World store in this plan implements exactly that, and the constructor does not change.

### Measurements behind the design (dev cluster, 2068 query-result series, 20.7k raw series, 35.6M samples)

1. **ClickHouse PromQL gives the same answers.** All 393 constructor queries ran through Coroot's engine and through ClickHouse 26.9 on the same data. Over 1 h at 15 s step: 86 of 87 non-empty queries identical (every point equal), 305 empty on both sides, 1 error: `timestamp()` is not implemented (only `mongo_rs_last_applied_timestamp_ms` uses it). Over 24 h at 15 min step: 74 identical; 15 differ by 1–2 series that disappeared inside the window (ClickHouse returns NaN points where Prometheus applies staleness); every point present on both sides is equal. Coroot's own querier returns labels with empty values; ClickHouse omits them, which is the PromQL semantics.
2. **ClickHouse PromQL is slower than Coroot's engine when used naively:** all 393 queries for 1 h take 5.2 s / 5.9 CPU-s in ClickHouse versus 3.2 s wall (2.9 CPU-s in ClickHouse plus Go) for Coroot's engine.
3. **Its cost is a fixed ~11 ms / ~14 CPU-ms per query, independent of the time range:** an instant query costs the same as a 1 h range (7.2 vs 7.5 CPU-s for all queries); a 4-step range costs 2.3 CPU-s for the 87 non-empty queries versus 1.9 CPU-s for one step; cutting retention from 4 days to 1 day changes read bytes by only 18 %.
4. **305 of 393 queries are for metrics that do not exist and still cost 4.3 of 6.2 CPU-s per pass.** Skipping them leaves 87 queries, 1.9–2.3 CPU-s per evaluation.
5. **Reading precomputed results is fast:** one page-style read of all query results from a `world_points`-shaped table (hour-first sort key, one row per series with `groupArray`, rollups for long windows), measured from the ClickHouse host: 1 h 20.6 ms, 24 h 25.6 ms, 7 d 35.6 ms at dev scale; 1 h 202 ms at 33k series and 600 ms at 103k series. Today the World loads from the disk cache in 5–8 ms, but the cache updater makes pages 80–100 ms for ~60 % of the time and writes ~50 GB/day to disk.

### The trick that keeps it fast

PromQL in ClickHouse is paid per query, not per point. So:

- evaluate a query **once per cycle over all the steps it is missing** (normally 2 steps at a 30 s cycle; 24 h on first start, in a single query each);
- **skip queries whose metrics do not exist** (selector names checked against `timeSeriesTags`);
- **never run PromQL for a page**: pages read `world_points` (or its 5 min / 1 h rollups) in one query;
- run PromQL at request time only for things that are not in the World: dashboards, the Prometheus HTTP API, MCP tools, explore.

Expected cost: 87 queries × ~22 CPU-ms = ~2 CPU-s per 30 s cycle (~0.07 core) instead of today's ~0.27 core of updater queries, no disk writes, and no lost history when a pod restarts (the world table lives in ClickHouse).

### Decisions taken in this plan (confirm before Task 9)

- **D1. ClickHouse always runs as a cluster** (decided 2026-10-05; already done outside this plan: `ch.LowLevelClient` refuses a ClickHouse without Keeper and `remote_servers`, every table is replicated and created `ON CLUSTER`, every read goes through a `Distributed` table; `make dev` runs 2 shards × 1 replica plus one Keeper). Everything in this plan must work on that 2-shard dev cluster. Two consequences found while switching:
  - a subquery over a `Distributed` table inside a query that itself runs on the shards (`... FROM x_distributed WHERE h IN (SELECT h FROM y_distributed)`) fails with `LOGICAL_ERROR: Sending a distributed query with unknown (zero) client version`; today's `prom/clickhouse_querier.go` has exactly that and is broken on the dev cluster until Task 4 replaces it. Use either the local table (when both tables are sharded by the same key) or `GLOBAL IN` / `GLOBAL JOIN`, as `clickhouse/queries.go` does for profiles;
  - `system.query_log` is per node: tests read `clusterAllReplicas(coroot, system.query_log)` with `is_initial_query`; `DELETE` / `DROP` in tests use `ON CLUSTER coroot`.
- **D2. One TimeSeries table per ClickHouse database, replicated, read through a Distributed table** (projects already have their own database), named `metrics`.
- **D3. Rollups reproduce today's cache exactly.** Today the cache stores 15 s points and widens them to the page step with the query's `FillFunc` (`timeseries/timeseries.go:165-254`): `FillAny` (the default, almost all 393 queries) keeps the **last** non-NaN value of the bucket, `FillAvg` (6 JVM/Go allocation and lock rates) the average, `FillSum` (`rr_application_log_messages`) the sum. Buckets are closed on the right: the point stamped T covers (T − step, T]. The rollups `world_points_5m/1h` therefore keep `Last` (`argMax(Value, Timestamp)`), `Sum`, `Cnt` and `Max` per bucket, bucketed on the right, and the store picks `Last`, `Sum/Cnt` or `Sum` from the `fillFunc` argument of `QueryRange`. An average for every query was considered and rejected: it changes rate and gauge charts against today and breaks raw counters (`container_restarts_total % 10000000`, `container_oom_kills_total`), whose deltas the constructor computes between consecutive points. `Max` is stored now but only read by Task 11.

## Review Focus

1. A series that stops inside a cycle (pod deleted): ClickHouse returns trailing NaN points; they must not be written to `world_points`, otherwise charts show gaps as values and Count-based rollups shift. Test in Task 4.
2. Re-evaluating a window twice (evaluator restart in the middle of a cycle) must not change what a page shows: raw reads keep one value per (series, ts), rollup averages are unaffected by duplicates, `FillSum` rollups are not — dedupe on write by evaluating strictly after `max(Timestamp)`. Test in Task 5.
3. A metric that appears after start (new application with Postgres) must get its query evaluated from its first sample, not from "now". Test in Task 5.
4. A page whose `to` is later than the last evaluated step must end at the last evaluated step (like today's `cacheTo` clamp), not show a zero tail. Test in Task 3.
5. Bucket alignment: a 5 min point stamped 10:05 must aggregate (10:00, 10:05], like `timeseries.FillAny`; `toStartOfInterval` alone gives [10:05, 10:10) and shifts every long-range chart by one step. Test in Task 3 (the store's output is compared with `FillAny` / `FillAvg` / `FillSum` applied in Go to the same 15 s points).
6. Labels with empty values: matchers like `{app_id=""}` (used by 70 `or` queries) must still select series without `app_id`, and the constructor must read a missing label as `""`. Test in Task 2.

---

## File Structure

| file | responsibility |
|---|---|
| `ch/client.go` | DDL: drop `metrics_*`, add `metrics` (TimeSeries), `world_series`, `world_points`, rollups + MVs |
| `ch/settings.go` (new) | `TimeSeriesSettings` for ch-go and clickhouse-go |
| `collector/metrics.go` | remote write → `INSERT INTO metrics (metric_name, tags, samples)` and metric families |
| `promql/client.go` (new, replaces `prom/`) | PromQL over ClickHouse: `QueryRange`, `Query`, label values, series, metadata |
| `promql/http.go` (new) | Prometheus-compatible HTTP handlers used by `api/prometheus.go` |
| `promql/selectors.go` (new) | metric names a query reads (parser only) |
| `world/store.go` (new) | `constructor.Cache` over `world_points` / rollups |
| `world/evaluator.go` (new) | the 30 s cycle: pick queries, evaluate, write, recording rules, notify |
| `world/queries.go` (new) | the list of queries to evaluate for a project (constructor queries, connection aggregations, custom SLIs) |
| `e2e/storage/promql_compat_test.go` (new) | guards ClickHouse PromQL semantics on every CH upgrade |
| `e2e/storage/world_test.go` (new) | evaluator + store against a real ClickHouse |
| deleted | `cache/`, `prom/`, `metrics_*` DDL, `IntegrationPrometheus` (Go + Vue), cluster-mode DDL |

---

### Task 1: ClickHouse PromQL compatibility suite

The suite pins every semantic this plan relies on, so a ClickHouse bump that changes them fails loudly.

**Files:**
- Create: `ch/settings.go`
- Create: `e2e/storage/promql_compat_test.go`

**Interfaces:**
- Produces: `ch.TimeSeriesSettings` (`[]chproto.Setting` for ch-go) and `ch.TimeSeriesContext(ctx) context.Context` (clickhouse-go).

- [ ] **Step 1: Settings helper**

```go
// ch/settings.go
package ch

import (
	"context"

	chgo "github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/clickhouse-go/v2"
)

// The TimeSeries engine and the timeSeries* functions behind PromQL are a
// private preview in 26.9 and need these settings on every query.
var TimeSeriesSettings = []chgo.Setting{
	chgo.SettingInt("enable_time_series_table", 1),
	chgo.SettingInt("enable_time_series_aggregate_functions", 1),
}

func TimeSeriesContext(ctx context.Context) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"enable_time_series_table":               1,
		"enable_time_series_aggregate_functions": 1,
	}))
}
```

- [ ] **Step 2: Write the compatibility test**

The test creates its own TimeSeries table in the `chtest` database, inserts hand-made samples, and checks results of `prometheusQueryRange` against hand-computed values.

```go
//go:build e2e

package storage

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// t0 is aligned to 15 s; samples are every 15 s for 10 minutes.
var compatT0 = time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)

func compatEnv(t *testing.T) *clickhouse.Client {
	e := chtest.New(t)
	e.Exec(t, "SET enable_time_series_table = 1")
	ctx := ch.TimeSeriesContext(context.Background())
	require.NoError(t, e.LL.Exec(ctx, "CREATE TABLE compat ENGINE = TimeSeries"))
	// counter c{app="a"} +15 per sample (rate 1/s); gauge g{app="a",empty=""} = 5;
	// gauge stop{app="b"} only for the first 2 minutes
	require.NoError(t, e.LL.Exec(ctx, `
INSERT INTO compat (metric_name, tags, samples)
SELECT 'c', map('app','a'), arrayMap(i -> (toDateTime64('2026-10-05 08:00:00',3) + toIntervalSecond(i*15), toFloat64(i*15)), range(40))
UNION ALL SELECT 'g', map('app','a','empty',''), arrayMap(i -> (toDateTime64('2026-10-05 08:00:00',3) + toIntervalSecond(i*15), 5.0), range(40))
UNION ALL SELECT 'stop', map('app','b'), arrayMap(i -> (toDateTime64('2026-10-05 08:00:00',3) + toIntervalSecond(i*15), 1.0), range(8))`))
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

type point struct {
	t time.Time
	v float64
}

func queryRange(t *testing.T, c *clickhouse.Client, q string, from, to time.Time) map[string][]point {
	t.Helper()
	rows, err := c.Query(ch.TimeSeriesContext(context.Background()),
		"SELECT tags, samples FROM prometheusQueryRange(currentDatabase(), 'compat', @q, @from, @to, 15)",
		clickhouse.Named("q", q), clickhouse.Named("from", from.Unix()), clickhouse.Named("to", to.Unix()))
	require.NoError(t, err, q)
	defer rows.Close()
	out := map[string][]point{}
	for rows.Next() {
		// Array(Tuple(String, String)) and Array(Tuple(DateTime64(3), Float64))
		// scan into [][]any in clickhouse-go v2; ClickHouse returns tags sorted
		var tags, samples [][]any
		require.NoError(t, rows.Scan(&tags, &samples))
		k := ""
		for _, kv := range tags {
			k += kv[0].(string) + "=" + kv[1].(string) + ","
		}
		for _, s := range samples {
			out[k] = append(out[k], point{s[0].(time.Time), s[1].(float64)})
		}
	}
	return out
}

func TestPromQLCompat(t *testing.T) {
	c := compatEnv(t)
	from, to := compatT0.Add(time.Minute), compatT0.Add(5*time.Minute)

	t.Run("rate over 45s", func(t *testing.T) {
		res := queryRange(t, c, `rate(c[45s])`, from, to)
		require.Len(t, res, 1)
		for _, p := range res["app=a,"] {
			assert.InDelta(t, 1.0, p.v, 1e-9, p.t)
		}
	})
	t.Run("sum by and or with an empty-label matcher", func(t *testing.T) {
		res := queryRange(t, c, `sum by (app) (g{empty!=""}) or g{empty=""}`, from, to)
		require.Len(t, res, 1, "an empty label is an absent label")
		_, ok := res["__name__=g,app=a,"]
		assert.True(t, ok, "%v", res)
	})
	t.Run("a series that stops yields NaN or nothing, never a value", func(t *testing.T) {
		// the last sample of stop is at t0+105s; past the 5 min lookback
		// (t0+6m45s) Prometheus has no point at all
		res := queryRange(t, c, `stop`, compatT0.Add(time.Minute), compatT0.Add(10*time.Minute))
		for _, p := range res["__name__=stop,app=b,"] {
			if p.t.After(compatT0.Add(105*time.Second + 5*time.Minute)) {
				assert.True(t, math.IsNaN(p.v), "stale point %v", p)
			}
		}
	})
	t.Run("one query, many steps: the range is evaluated in one call", func(t *testing.T) {
		res := queryRange(t, c, `c`, from, to)
		assert.Len(t, res["__name__=c,app=a,"], 17) // 1m..5m inclusive at 15 s
	})
	t.Run("time() replaces timestamp()", func(t *testing.T) {
		res := queryRange(t, c, `time() - g`, from, to)
		require.Len(t, res, 1)
		p := res["app=a,"][0]
		assert.InDelta(t, float64(p.t.Unix())-5, p.v, 1e-9)
	})
}
```

- [ ] **Step 3: The same on the cluster (blocking for the whole plan)**

Repeat the subtests against a TimeSeries table created `ON CLUSTER` with replicated inner tables and read through a `Distributed` table over it, with series spread over both shards:

```sql
CREATE TABLE compat ON CLUSTER coroot ENGINE = TimeSeries
SAMPLES INNER ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/{database}/compat_samples', '{replica}') ORDER BY (id, timestamp)
TAGS INNER ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/{database}/compat_tags', '{replica}') PRIMARY KEY metric_name ORDER BY (metric_name, id)
```

Insert `c{app="a"}` on shard 1 and `g` on shard 2 (connect to each node, `clickhouse-0` / `clickhouse-1`), then check that `prometheusQueryRange` returns both. Find out, and write down here, which of these works: `prometheusQueryRange` over the local TimeSeries table on the initiator (expected: sees one shard only), over a `Distributed` table whose underlying table is TimeSeries, or `clusterAllReplicas`/`remote` around the samples. If none gives cluster-wide results with correct `sum by` across shards, stop and come back to the human: the design of Tasks 2–5 depends on it (a fallback is to keep all series of a metric on one shard by sharding on `sipHash64(metric_name)`, so every non-aggregating query is shard-local, and aggregate across shards in the evaluator).

- [ ] **Step 4: Run it**

Run: `make test-e2e TestPromQLCompat`
Expected: PASS on 26.9.10.4. If the stale-point subtest fails because ClickHouse starts applying staleness, keep the assertion (the evaluator drops NaN either way) and loosen it to "NaN or absent".

- [ ] **Step 5: Commit**

```bash
git add ch/settings.go e2e/storage/promql_compat_test.go
git commit -m "test: pin ClickHouse PromQL semantics Coroot relies on"
```

---

### Task 2: TimeSeries table and the collector writing into it

**Files:**
- Modify: `ch/client.go` (tables list, `distributedTables`, `ReplaceTables`)
- Modify: `collector/metrics.go`
- Modify: `collector/metrics_test.go`
- Modify: `e2e/storage/metrics_test.go` (round trip now through PromQL in ClickHouse, see Task 4 for the client; this task asserts the raw rows)

**Interfaces:**
- Consumes: `ch.TimeSeriesSettings` (Task 1).
- Produces: table `metrics` (TimeSeries); `collector.NewMetricsBatch(limit int, timeout time.Duration, exec func(chgo.Query) error) *MetricsBatch` with the same signature as today.

- [ ] **Step 1: Replace the three metrics tables in `ch/client.go`**

Delete the `metrics_samples`, `metrics_series`, `metrics_metadata` CREATE statements, their `_distributed` entries and their names in `ReplaceTables`. Add (note: `Migrate` must run this statement with `ch.TimeSeriesSettings`; add a `settings []chgo.Setting` field to the tables entry or run TimeSeries DDL in a separate list `timeSeriesTables` executed with the settings):

```go
var timeSeriesTables = []string{
	`CREATE TABLE IF NOT EXISTS metrics ENGINE = TimeSeries
SETTINGS recent_samples_ttl_seconds = 0
SAMPLES INNER ENGINE = MergeTree ORDER BY (id, timestamp) TTL toDateTime(timestamp) + toIntervalSecond(@ttl_metrics) SETTINGS ttl_only_drop_parts = 1`,
}
```

`recent_samples_ttl_seconds = 0` disables the second copy of the last 4 days: the evaluator reads only the last minutes and pages do not read raw samples at all, so the extra copy buys nothing. If the 26.9 grammar rejects `SAMPLES INNER ENGINE ... TTL`, create the table with defaults and set the TTL with `ALTER TABLE metrics MODIFY ... ` on the inner samples table (`.inner_id.samples.<uuid>`, found with `SELECT name FROM system.tables WHERE database = currentDatabase() AND name LIKE '.inner_id.samples.%'`).

- [ ] **Step 2: Write the failing collector test**

```go
// collector/metrics_test.go
func TestMetricsBatchWritesTimeSeriesRows(t *testing.T) {
	var queries []chgo.Query
	b := NewMetricsBatch(1_000_000, time.Hour, func(q chgo.Query) error { queries = append(queries, q); return nil })
	b.Add(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		{Labels: []prompb.Label{{Name: "__name__", Value: "up"}, {Name: "job", Value: "a"}, {Name: "empty", Value: ""}},
			Samples: []prompb.Sample{{Timestamp: 1_000, Value: 1}, {Timestamp: 16_000, Value: 0}}},
	}})
	b.Close()
	require.Len(t, queries, 1)
	assert.Equal(t, insertSamplesSQL, queries[0].Body)
	assert.Equal(t, ch.TimeSeriesSettings, queries[0].Settings)
	// one row per series, the samples as two parallel arrays; the empty label is dropped
	in := queries[0].Input
	require.Equal(t, []string{"name", "tags", "ts", "vs"}, []string{in[0].Name, in[1].Name, in[2].Name, in[3].Name})
	assert.Equal(t, "up", in[0].Data.(*chproto.ColStr).Row(0))
	assert.Equal(t, map[string]string{"job": "a"}, in[1].Data.(*chproto.ColMap[string, string]).Row(0))
	assert.Len(t, in[2].Data.(*chproto.ColArr[time.Time]).Row(0), 2)
	assert.Equal(t, []float64{1, 0}, in[3].Data.(*chproto.ColArr[float64]).Row(0))
}
```

- [ ] **Step 3: Run it to see it fail**

Run: `go test ./collector -run TestMetricsBatchWritesTimeSeriesRows -v`
Expected: FAIL (body is the old `metrics_series` insert).

- [ ] **Step 4: Rewrite `MetricsBatch`**

ch-go has no generic column for `Array(Tuple(DateTime64(3), Float64))`, so the batch sends two parallel arrays and ClickHouse zips them (`INSERT ... SELECT ... FROM input(...)` works over the native protocol):

```go
const insertSamplesSQL = `INSERT INTO metrics (metric_name, tags, samples)
SELECT name, tags, arrayZip(ts, vs) FROM input('name String, tags Map(String, String), ts Array(DateTime64(3)), vs Array(Float64)')`
```

Columns of the batch: `name *chproto.ColStr`, `tags *chproto.ColMap[string, string]` (`chproto.NewMap[string, string](new(chproto.ColStr), new(chproto.ColStr))`), `ts *chproto.ColArr[time.Time]` (`chproto.NewArray[time.Time](new(chproto.ColDateTime64).WithPrecision(chproto.PrecisionMilli))`), `vs *chproto.ColArr[float64]` (`chproto.NewArray[float64](new(chproto.ColFloat64))`). In `Add`, for each `prompb.TimeSeries`: `__name__` is the name, every other label with a non-empty value goes to tags, and one row carries all its samples (`time.UnixMilli(s.Timestamp)`, `s.Value`). In `save()`:

```go
q := chgo.Query{
	Body:     insertSamplesSQL,
	Settings: ch.TimeSeriesSettings,
	Input: chproto.Input{
		{Name: "name", Data: b.name},
		{Name: "tags", Data: b.tags},
		{Name: "ts", Data: b.ts},
		{Name: "vs", Data: b.vs},
	},
}
```

Metadata (`req.GetMetadata()`) goes to `INSERT INTO metrics (metric_family, type, unit, help) VALUES` in the same `save()` (a second query, only when non-empty). Delete the `series` map, `metricSeries`, `LabelsToSignature` use and the series dedupe — TimeSeries keeps one tags row per series itself.

- [ ] **Step 5: Run unit tests**

Run: `go test ./collector -v`
Expected: PASS.

- [ ] **Step 6: Raw-row e2e check**

In `e2e/storage/metrics_test.go`, replace the body of `TestMetricsRoundTrip` up to the PromQL part with: write the existing fixture through `collector.NewMetricsBatch(...).Add(...)`, then assert `SELECT count() FROM timeSeriesTags(currentDatabase(), 'metrics')` is 5 and `SELECT count() FROM timeSeriesSamples(currentDatabase(), 'metrics')` is 5 × 41 (send `ch.TimeSeriesSettings`). Also assert that a series written in two batches has one tags row after `OPTIMIZE ... FINAL` (Review Focus 6 for the empty label: insert a label with an empty value and check it is absent from `tags`).

Run: `make test-e2e TestMetricsRoundTrip`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add ch/client.go collector/metrics.go collector/metrics_test.go e2e/storage/metrics_test.go
git commit -m "feat: store metrics in a ClickHouse TimeSeries table"
```

---

### Task 3: World tables and the World store (reads only)

**Files:**
- Modify: `ch/client.go`
- Create: `world/store.go`
- Create: `world/store_test.go` (SQL shape, unit)
- Create: `e2e/storage/world_test.go`

**Interfaces:**
- Produces:
  - tables `world_series`, `world_points`, `world_points_5m`, `world_points_1h` (+ `_mv`s)
  - `world.NewStore(c *clickhouse.Client) *Store`
  - `(*Store) QueryRange(ctx, query string, from, to timeseries.Time, step timeseries.Duration, fillFunc timeseries.FillFunc) ([]*model.MetricValues, error)` — satisfies `constructor.Cache`
  - `(*Store) GetStep(from, to timeseries.Time) (timeseries.Duration, error)` — returns 15 s
  - `(*Store) LastEvaluated(ctx) (timeseries.Time, error)` — `max(Timestamp)` of `world_points`
  - `(*Store) Load(ctx, from, to timeseries.Time, step timeseries.Duration) (map[string][]*model.MetricValues, error)` — all queries in one read; `QueryRange` filters it

- [ ] **Step 1: DDL (append to `tables` in `ch/client.go`)**

```sql
CREATE TABLE IF NOT EXISTS world_series (
    Query LowCardinality(String),
    SeriesHash UInt64,
    Labels Map(LowCardinality(String), String),
    LastSeen DateTime('UTC')
) ENGINE ReplacingMergeTree(LastSeen)
ORDER BY (Query, SeriesHash)
TTL LastSeen + toIntervalSecond(@ttl_metrics)
SETTINGS index_granularity = 256

CREATE TABLE IF NOT EXISTS world_points (
    Query LowCardinality(String),
    SeriesHash UInt64,
    Timestamp DateTime('UTC') CODEC(DoubleDelta, ZSTD(1)),
    Value Float32 CODEC(Gorilla, ZSTD(1))
) ENGINE MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (toStartOfHour(Timestamp), Query, SeriesHash, Timestamp)
TTL Timestamp + toIntervalSecond(@ttl_metrics)
SETTINGS ttl_only_drop_parts = 1

CREATE TABLE IF NOT EXISTS world_points_5m (
    Query LowCardinality(String),
    SeriesHash UInt64,
    Timestamp DateTime('UTC'),                          -- end of the bucket: covers (Timestamp - 5m, Timestamp]
    Last AggregateFunction(argMax, Float32, DateTime('UTC')),
    Sum SimpleAggregateFunction(sum, Float64),
    Cnt SimpleAggregateFunction(sum, UInt64),
    Max SimpleAggregateFunction(max, Float32)
) ENGINE AggregatingMergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (toStartOfDay(Timestamp), Query, SeriesHash, Timestamp)
TTL Timestamp + toIntervalSecond(@ttl_metrics)

CREATE MATERIALIZED VIEW IF NOT EXISTS world_points_5m_mv TO world_points_5m AS
SELECT Query, SeriesHash, Bucket AS Timestamp,
       argMaxState(Value, Ts) AS Last, sum(toFloat64(Value)) AS Sum, count() AS Cnt, max(Value) AS Max
FROM (SELECT Query, SeriesHash, Value, Timestamp AS Ts,
             toStartOfInterval(Timestamp - INTERVAL 1 SECOND, INTERVAL 5 MINUTE) + INTERVAL 5 MINUTE AS Bucket
      FROM world_points)
GROUP BY Query, SeriesHash, Bucket
```

`world_points_1h` / `world_points_1h_mv`: the same with `INTERVAL 1 HOUR`, also reading `world_points` (not the 5 min rollup). The inner subquery renames `Timestamp` to `Ts` so that `argMaxState` sees the point's time and not the bucket alias. The sort keys are the layouts measured in "Design and evidence" point 5 (`ORDER BY (Query, SeriesHash, Timestamp)` without the hour read 494M rows for 7 d; the hour-first key 0.35M). That measurement used `Sum`/`Cnt` only; `Last` adds an `argMax` state per row (re-measure in Task 7 Step 6).

- [ ] **Step 2: Write the failing SQL-shape test**

```go
// world/store_test.go
func TestStoreSQLPicksTableByStep(t *testing.T) {
	from, to := timeseries.Time(1_790_000_100), timeseries.Time(1_790_003_700)
	raw := pointsSQL(from, to, 15)
	assert.Contains(t, raw, "FROM @@table_world_points@@")
	assert.Contains(t, raw, "toStartOfHour(Timestamp) BETWEEN")
	// right-closed window: the first point covers (from - step, from]
	assert.Contains(t, raw, "Timestamp > toDateTime(1790000085) AND Timestamp <= toDateTime(1790003700)")

	r5 := pointsSQL(from, to, 15*timeseries.Minute)
	assert.Contains(t, r5, "FROM @@table_world_points_5m@@")
	assert.Contains(t, r5, "argMaxMerge(Last)")
	assert.Contains(t, r5, "toStartOfInterval(Timestamp - INTERVAL 1 SECOND, INTERVAL 900 SECOND) + INTERVAL 900 SECOND")

	assert.Contains(t, pointsSQL(from, to, timeseries.Hour), "FROM @@table_world_points_1h@@")
	assert.Contains(t, pointsSQL(from, to, 2*timeseries.Hour), "FROM @@table_world_points_1h@@")
	// a step that is not a multiple of 5 min buckets raw points
	r2 := pointsSQL(from, to, 2*timeseries.Minute)
	assert.Contains(t, r2, "FROM @@table_world_points@@")
	assert.Contains(t, r2, "argMax(Value, Timestamp)")
	// one row per series, three aggregates per point
	assert.Contains(t, raw, "GROUP BY Query, SeriesHash")
	assert.Contains(t, raw, "AS Ls, groupArray(toFloat32(a)) AS As, groupArray(toFloat32(s)) AS Ss")
}

func TestPickValues(t *testing.T) {
	assert.Equal(t, aggLast, aggregateFor(timeseries.FillAny))
	assert.Equal(t, aggLast, aggregateFor(nil))
	assert.Equal(t, aggAvg, aggregateFor(timeseries.FillAvg))
	assert.Equal(t, aggSum, aggregateFor(timeseries.FillSum))
}
```

- [ ] **Step 3: Run it to see it fail**

Run: `go test ./world -run TestStoreSQLPicksTableByStep -v`
Expected: FAIL (package does not exist).

- [ ] **Step 4: Implement `world/store.go`**

```go
package world

type aggregate int

const (
	aggLast aggregate = iota // timeseries.FillAny: last value of the bucket
	aggAvg                   // timeseries.FillAvg
	aggSum                   // timeseries.FillSum
)

// aggregateFor maps the constructor's fill function to the rollup column that
// gives the same value today's cache gives. Funcs are compared by pointer.
func aggregateFor(f timeseries.FillFunc) aggregate {
	switch reflect.ValueOf(f).Pointer() {
	case reflect.ValueOf(timeseries.FillAvg).Pointer():
		return aggAvg
	case reflect.ValueOf(timeseries.FillSum).Pointer():
		return aggSum
	}
	return aggLast
}

// pointsSQL returns one row per (Query, SeriesHash) with the points of the
// window at the page step. A point stamped T covers (T - step, T], like
// timeseries.FillAny. Each point carries the three aggregates (last, average,
// sum); the store picks one per query.
func pointsSQL(from, to timeseries.Time, step timeseries.Duration) string {
	bucket := fmt.Sprintf("toStartOfInterval(Timestamp - INTERVAL 1 SECOND, INTERVAL %[1]d SECOND) + INTERVAL %[1]d SECOND", step)
	window := fmt.Sprintf("Timestamp > toDateTime(%d) AND Timestamp <= toDateTime(%d)", from.Add(-step), to)
	var src, last, avg, sum, prune string
	switch {
	case step >= timeseries.Hour && step%timeseries.Hour == 0:
		src, last, avg, sum = "@@table_world_points_1h@@", "argMaxMerge(Last)", "sum(Sum)/sum(Cnt)", "sum(Sum)"
		prune = "toStartOfDay(Timestamp) BETWEEN toStartOfDay(toDateTime(%d)) AND toStartOfDay(toDateTime(%d))"
	case step >= 5*timeseries.Minute && step%(5*timeseries.Minute) == 0:
		src, last, avg, sum = "@@table_world_points_5m@@", "argMaxMerge(Last)", "sum(Sum)/sum(Cnt)", "sum(Sum)"
		prune = "toStartOfDay(Timestamp) BETWEEN toStartOfDay(toDateTime(%d)) AND toStartOfDay(toDateTime(%d))"
	default:
		src, last, avg, sum = "@@table_world_points@@", "argMax(Value, Timestamp)", "avg(Value)", "sum(Value)"
		prune = "toStartOfHour(Timestamp) BETWEEN toStartOfHour(toDateTime(%d)) AND toStartOfHour(toDateTime(%d))"
	}
	prune = fmt.Sprintf(prune, from.Add(-step), to)
	return fmt.Sprintf(`SELECT Query, SeriesHash, groupArray(toUInt32(t)) AS Ts, groupArray(toFloat32(l)) AS Ls, groupArray(toFloat32(a)) AS As, groupArray(toFloat32(s)) AS Ss
FROM (SELECT Query, SeriesHash, %s AS t, %s AS l, %s AS a, %s AS s FROM %s WHERE %s AND %s GROUP BY Query, SeriesHash, t)
GROUP BY Query, SeriesHash`, bucket, last, avg, sum, src, prune, window)
}
```

For a 15 s step on raw points `bucket` is the point's own timestamp, so the three aggregates are equal; that keeps one code path.

`Load` runs `SELECT Query, SeriesHash, Labels FROM @@table_world_series@@ FINAL`, then `pointsSQL`, with ch-go columnar decoding as in the measured benchmark (`$CLAUDE_JOB_DIR/tmp/audit-world/reader/main.go`, function `load`): one map lookup per series, then one `timeseries.TimeSeries` per aggregate is not needed — keep the three `[]float32` arrays per series in the loaded result and build the `TimeSeries` in `QueryRange` from `Ls`, `As` or `Ss` according to `aggregateFor(fillFunc)`. The constructor asks for SLI queries a second time under `<name>_raw` with the raw step (`constructor.queryCache`); the store serves `<name>_raw` from the rows of `<name>`. `QueryRange(query, ...)` reads from `Load(...)` (cache the last `Load` result per `(from, to, step)` for the duration of one constructor run: `LoadWorld` calls `QueryRange` ~400 times with the same window — add a `sync.Once`-guarded field keyed by the window).

`QueryRange` clamps `to` to `LastEvaluated` (Review Focus 4).

- [ ] **Step 5: Run unit tests**

Run: `go test ./world -v`
Expected: PASS.

- [ ] **Step 6: e2e: store reads what was written**

```go
//go:build e2e

// The store must give, at every page step, exactly what today's cache gives:
// 15 s points widened with the query's FillFunc. The expected values are made
// by running timeseries.FillAny / FillAvg / FillSum in Go on the same points.
func TestWorldStoreMatchesFillFuncs(t *testing.T) {
	e := chtest.New(t)
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	t.Cleanup(c.Close)

	t0 := timeseries.TimeFromStandard(time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour))
	const n = 720 // 3 h of 15 s points, value i at t0 + 15i, with a gap
	vals := make([]float32, n)
	for i := range vals {
		vals[i] = float32(i)
		if i >= 100 && i < 130 { // 7.5 min without data
			vals[i] = timeseries.NaN
		}
	}
	e.Exec(t, `INSERT INTO world_series VALUES ('q', 1, map('app','a'), now())`)
	var rows []string
	for i, v := range vals {
		if !timeseries.IsNaN(v) {
			rows = append(rows, fmt.Sprintf("('q', 1, toDateTime(%d), %g)", t0.Add(timeseries.Duration(i)*15), v))
		}
	}
	e.Exec(t, "INSERT INTO world_points VALUES "+strings.Join(rows, ","))

	s := world.NewStore(c)
	ctx := context.Background()
	from, to := t0.Add(timeseries.Hour), t0.Add(3*timeseries.Hour)
	for _, step := range []timeseries.Duration{15, 2 * timeseries.Minute, 5 * timeseries.Minute, 15 * timeseries.Minute, timeseries.Hour} {
		for name, fill := range map[string]timeseries.FillFunc{"any": timeseries.FillAny, "avg": timeseries.FillAvg, "sum": timeseries.FillSum} {
			want := timeseries.New(from.Truncate(step), int(to.Truncate(step).Sub(from.Truncate(step))/step)+1, step)
			fill(want, t0, 15, vals)

			res, err := s.QueryRange(ctx, "q", from, to, step, fill)
			require.NoError(t, err)
			require.Len(t, res, 1)
			assert.Equal(t, "a", res[0].Labels["app"])
			gotJSON, _ := res[0].Values.MarshalJSON()
			wantJSON, _ := want.MarshalJSON()
			assert.JSONEq(t, string(wantJSON), string(gotJSON), "step %d, %s", step, name)
		}
	}

	// Review Focus 4: a window past the last evaluated point ends there
	res, _ := s.QueryRange(ctx, "q", from, to.Add(timeseries.Hour), 15, timeseries.FillAny)
	assert.False(t, timeseries.IsNaN(res[0].Values.Last()), "the tail past the last point is clamped, not NaN")
}
```

If `FillAvg` differs from `Sum/Cnt` on buckets that are partly empty, read `timeseries.FillAvg` (`timeseries/timeseries.go:256`) and match its rule (average of the non-NaN points of the bucket is what `Sum/Cnt` computes, since NaN points are never written).

Run: `make test-e2e TestWorldStoreMatchesFillFuncs`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add ch/client.go world/ e2e/storage/world_test.go
git commit -m "feat: world tables with rollups and a store the constructor can read"
```

---

### Task 4: PromQL client over ClickHouse

**Files:**
- Create: `promql/client.go`, `promql/selectors.go`, `promql/http.go`
- Create: `promql/selectors_test.go`, `promql/http_test.go`
- Modify: `e2e/storage/metrics_test.go` (PromQL part now through `promql.Client`)

**Interfaces:**
- Consumes: `ch.TimeSeriesSettings`, table `metrics`.
- Produces:
  - `promql.New(c *clickhouse.Client) *Client`
  - `(*Client) QueryRange(ctx, query string, from, to timeseries.Time, step timeseries.Duration) ([]*model.MetricValues, error)` — `$RANGE` → `3*step`; NaN points dropped; series with no point dropped; labels without `__name__`
  - `(*Client) MetricNames(ctx) (map[string]bool, error)` — `SELECT DISTINCT metric_name FROM timeSeriesTags(currentDatabase(), 'metrics')`
  - `promql.Selectors(query string) ([]string, error)` — metric names a query reads
  - `(*Client) QueryRangeHandler / LabelValues / Series / MetricMetadata(w, r)` — Prometheus HTTP API JSON, same shapes `prom/clickhouse.go` writes today

- [ ] **Step 1: Failing test for selectors**

```go
func TestSelectors(t *testing.T) {
	for q, want := range map[string][]string{
		`up`: {"up"},
		`sum by(app_id) (rate(container_http_requests_total{app_id!=""}[$RANGE])) or rate(container_http_requests_total{app_id=""}[$RANGE])`: {"container_http_requests_total"},
		`kube_pod_status_phase > 0`:                       {"kube_pod_status_phase"},
		`time() - mongo_rs_last_applied_timestamp_ms/1000`: {"mongo_rs_last_applied_timestamp_ms"},
		`{__name__=~"up|node_info"}`:                       nil, // unknown names: always evaluate
	} {
		got, err := Selectors(q)
		require.NoError(t, err, q)
		assert.ElementsMatch(t, want, got, q)
	}
}
```

- [ ] **Step 2: Run, see it fail; implement with `parser.ParseExpr(strings.ReplaceAll(q, "$RANGE", "1m"))` and `parser.Inspect` collecting `VectorSelector.Name`; return `nil` (meaning "can't tell, evaluate") when a selector has no name. Run again: PASS.**

Run: `go test ./promql -run TestSelectors -v`

- [ ] **Step 3: `QueryRange`**

```go
func (c *Client) QueryRange(ctx context.Context, query string, from, to timeseries.Time, step timeseries.Duration) ([]*model.MetricValues, error) {
	query = strings.ReplaceAll(query, "$RANGE", fmt.Sprintf("%ds", int64(step*3)))
	from, to = from.Truncate(step), to.Truncate(step)
	rows, err := c.ch.Query(ch.TimeSeriesContext(ctx),
		"SELECT tags, samples FROM prometheusQueryRange(currentDatabase(), 'metrics', @q, @from, @to, @step)",
		clickhouse.Named("q", query), clickhouse.Named("from", int64(from)), clickhouse.Named("to", int64(to)), clickhouse.Named("step", int64(step)))
	// decode: tags -> model.Labels without "__name__" and without empty values;
	// samples -> timeseries.New(from, int(to.Sub(from)/step)+1, step), skipping NaN;
	// drop series whose every point was NaN (Review Focus 1)
}
```

`Close()` is a no-op (the ClickHouse client is shared).

- [ ] **Step 4: HTTP handlers**

Port `QueryRangeHandler`, `LabelValues`, `Series`, `MetricMetadata` from `prom/clickhouse.go` keeping the response JSON byte-for-byte (they feed the front end's Prometheus-style panels). Sources: `QueryRange` above; label values from `SELECT DISTINCT tags[@name] FROM timeSeriesTags(currentDatabase(), 'metrics') WHERE max_time >= @from` (matchers resolved by evaluating the matcher selector with `prometheusQuery` and collecting tags); metadata from `timeSeriesMetricFamilies(currentDatabase(), 'metrics')`. Unit-test the JSON shape with a fake `QueryRange` (`http_test.go`, golden JSON copied from today's response of `/api/project/<id>/prom/api/v1/query_range` on the dev cluster).

- [ ] **Step 5: e2e round trip**

`TestMetricsRoundTrip` keeps its PromQL assertions (matchers, regex, `rate`, `sum by`, label values) but builds `promql.New(c)` instead of `prom.NewClient(...)`. Add Review Focus 6: `up{missing=""}` returns all 3 `up` series; `up{job=""}` returns none.

Run: `make test-e2e TestMetricsRoundTrip TestPromQLCompat`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add promql/ e2e/storage/metrics_test.go
git commit -m "feat: PromQL client that runs queries in ClickHouse"
```

---

### Task 5: The evaluator

**Files:**
- Create: `world/queries.go`, `world/evaluator.go`
- Create: `world/evaluator_test.go` (unit, fake ClickHouse), extend `e2e/storage/world_test.go`

**Interfaces:**
- Consumes: `promql.Client.QueryRange`, `promql.Client.MetricNames`, `promql.Selectors`, `world.Store.LastEvaluated`, tables from Task 3.
- Produces:
    - `constructor.EvaluatedQueries(project *db.Project, checkConfigs model.CheckConfigs, options map[constructor.Option]bool) []constructor.EvaluatedQuery`: extract the part of `constructor.queryCache` (`constructor/constructor.go:200-260`) that builds the `queries` map into this exported function returning `[]constructor.EvaluatedQuery{Name, Expr string; FillFunc timeseries.FillFunc}`; `queryCache` keeps calling it, so the names stay identical
  - `world.QueriesFor(project, checkConfigs) []constructor.EvaluatedQuery` — calls `constructor.EvaluatedQueries` with the options the evaluator needs (all queries; recording rules are computed in Task 6, not evaluated as PromQL)
  - `world.NewEvaluator(database *db.DB, clients func(*db.Project) (*clickhouse.Client, error), cycle time.Duration, backfill timeseries.Duration) *Evaluator`
  - `(*Evaluator) Run(ctx)`; `(*Evaluator) Updates() <-chan db.ProjectId` (same contract as `cache.Cache.Updates()` that `watchers/watchers.go:40` consumes)
  - `(*Evaluator) Status(projectId) Status{LastEvaluated timeseries.Time; Lag timeseries.Duration; Error string}`

- [ ] **Step 1: Failing unit test for one cycle**

```go
type fakeCH struct {
	names   map[string]bool
	calls   []string // expr|from|to
	results map[string][]*model.MetricValues
	written []writtenPoint
	last    map[string]timeseries.Time // per query
}

func TestCycleEvaluatesOnlyExistingMetricsAndOnlyNewSteps(t *testing.T) {
	f := &fakeCH{
		names: map[string]bool{"up": true},
		last:  map[string]timeseries.Time{"up": 1000},
	}
	queries := []constructor.EvaluatedQuery{{Name: "up", Expr: "up"}, {Name: "pg", Expr: "pg_up"}}
	cycle(context.Background(), f, queries, 1060, 24*timeseries.Hour)
	assert.Equal(t, []string{"up|1015|1060"}, f.calls, "pg_up does not exist; up is evaluated after its last point only")
}

func TestCycleBackfillsAQueryWithNoPoints(t *testing.T) {
	f := &fakeCH{names: map[string]bool{"pg_up": true}, last: map[string]timeseries.Time{}}
	cycle(context.Background(), f, []constructor.EvaluatedQuery{{Name: "pg", Expr: "pg_up"}}, 100_000, 24*timeseries.Hour)
	assert.Equal(t, []string{"pg_up|13600|100000"}, f.calls, "a new query is evaluated over the backfill window in one call")
}

func TestCycleDropsNaN(t *testing.T) {
	ts := timeseries.New(1015, 3, 15)
	ts.Set(1015, 1)
	ts.Set(1030, float32(math.NaN()))
	f := &fakeCH{names: map[string]bool{"up": true}, last: map[string]timeseries.Time{"up": 1000},
		results: map[string][]*model.MetricValues{"up": {{Labels: model.Labels{"job": "a"}, Values: ts}}}}
	cycle(context.Background(), f, []constructor.EvaluatedQuery{{Name: "up", Expr: "up"}}, 1045, 24*timeseries.Hour)
	require.Len(t, f.written, 1)
	assert.Equal(t, timeseries.Time(1015), f.written[0].t)
}
```

`cycle` takes an interface (`metricNames`, `queryRange`, `lastPerQuery`, `write`) so the fake fits; the real implementation wires `promql.Client` and ch-go inserts.

- [ ] **Step 2: Run, see them fail.**

Run: `go test ./world -run TestCycle -v`

- [ ] **Step 3: Implement**

`cycle(ctx, ch, queries, now, backfill)`:
1. `names := ch.metricNames()`; keep a query if `promql.Selectors(expr)` returns `nil` or any name in `names`.
2. `last := ch.lastPerQuery()` (`SELECT Query, max(Timestamp) FROM @@table_world_points@@ GROUP BY Query`, one query per cycle).
3. For each kept query, `from := last[q] + 15` or `now - backfill` if absent; `to := now.Truncate(15)`; skip if `from > to`; evaluate with concurrency 8 (`errgroup` with `SetLimit(8)`).
4. Write `world_series` rows (`Query`, `SeriesHash = labels.Hash()`, `Labels`, `LastSeen = to`) and `world_points` rows (non-NaN only) in two batched inserts per cycle.
5. After writing, send the project id on `Updates()` (non-blocking, buffered 1).

`Run` loops every 30 s over `database.GetProjects()`; per-project errors go to `Status` and the log, never stop the loop. Concurrency across projects: one at a time.

- [ ] **Step 4: Run unit tests: PASS.**

- [ ] **Step 5: e2e: evaluator against real ClickHouse**

In `e2e/storage/world_test.go`: write metrics through `collector.NewMetricsBatch` (fixture from `TestMetricsRoundTrip`), run `cycle` twice with `now` advanced by 30 s, and assert:
- `world_points` for query `up` has the points of both cycles and no duplicates (`SELECT count(), uniqExact(SeriesHash, Timestamp) FROM world_points WHERE Query = 'up'` equal) — Review Focus 2;
- rerunning the first cycle (restart) does not change `QueryRange` output for a page window — Review Focus 2;
- a metric inserted after the first cycle gets its query evaluated from its first sample (count of points equals samples within the backfill window) — Review Focus 3.

Run: `make test-e2e TestWorldEvaluator`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add world/ constructor/queries.go e2e/storage/world_test.go
git commit -m "feat: evaluator that keeps the world table up to date"
```

---

### Task 6: Recording rules and custom SLIs through the evaluator

**Files:**
- Modify: `world/evaluator.go`
- Modify: `constructor/queries.go` (`RecordingRules` unchanged in behaviour; only the call site moves)
- Test: `world/evaluator_test.go`, `e2e/storage/world_test.go`

**Interfaces:**
- Consumes: `constructor.RecordingRules` (`map[string]func(*db.DB, *db.Project, *model.World) []*model.MetricValues`), `constructor.New(...).LoadWorld(...)` with `world.Store` as the cache.
- Produces: rows of queries `rr_*` in `world_points` / `world_series`.

- [ ] **Step 1: Failing test**: after a cycle, `world_points` contains `rr_connection_*` / `rr_application_*` rows for the cycle's steps, computed by building the World for `[from, to]` from the store (copy the expectations of `cache/updater_test.go` that check recording rules, if any; otherwise assert that `rr_application_l7_requests` is present for the fixture app with the value from the fixture's `container_http_requests_total` rate).
- [ ] **Step 2: Implement**: at the end of `cycle`, for the evaluated window `[minFrom, to]` (capped at 1 h: recording rules over a 24 h backfill would build a huge World — for backfill, compute them per hour), build the World with `constructor.New(db, project, map[db.ProjectId]constructor.Cache{project.Id: store}, pricing, constructor.OptionDoNotLoadRawSLIs...)`, run each rule, write its `MetricValues` under the rule name. This is what `cache/updater.go:337 processRecordingRules` does today, with the store instead of the chunk cache.
- [ ] **Step 3: Custom SLIs**: `world.QueriesFor` adds per-app custom SLI queries (`qApplicationCustomSLI/<appId>/total_requests` …) from `checkConfigs`, exactly the names `constructor.queryCache` builds today, so the constructor finds them in the store.
- [ ] **Step 4: Run** `go test ./world ./constructor` and `make test-e2e TestWorldEvaluator`: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: recording rules and custom SLIs from the world table"`

---

### Task 7: Wire it in and delete the cache and the Prometheus engine

**Files:**
- Modify: `main.go` (create the evaluator instead of `cache.NewCache`), `api/api.go` (`LoadWorld`, `LoadWorldByRequest`, panel data, `GetPrometheusClient`-style call sites listed below), `api/prometheus.go`, `api/mcp.go:1145,1379`, `api/rca.go:65,230`, `collector/config.go:50`, `collector/collector.go`, `watchers/watchers.go`
- Delete: `cache/` (whole package), `prom/` (whole package)
- Modify: `go.mod` (drop `github.com/prometheus/prometheus` engine imports if nothing else uses them; `promql/parser` stays)

**Interfaces:**
- Consumes: `world.Evaluator`, `world.Store`, `promql.Client`.
- Produces: no new API; HTTP responses unchanged except `cacheStatus` → `metricsStatus` fields `{lag_max, lag_avg, error}` filled from `Evaluator.Status` (same JSON keys the front end reads today, so the front end does not change).

- [ ] **Step 1**: Replace every `api.cache.GetCacheClient(project.Id)` with `world.NewStore(chClient)` and every `prom.NewClient(...)` with `promql.New(chClient)`; the call sites are exactly those printed by `grep -rn "GetCacheClient\|prom.NewClient" --include=*.go . | grep -v _test`.
- [ ] **Step 2**: `LoadWorld`: `to` is clamped to `store.LastEvaluated` (as `cacheTo` was); `step` comes from `increaseStepForBigDurations(from, to, 15*timeseries.Second)` — `GetStep` is gone from the decision.
- [ ] **Step 3**: `watchers.Start` takes `updates <-chan db.ProjectId` and a `func(*db.Project) constructor.Cache` instead of `*cache.Cache`.
- [ ] **Step 4**: Delete `cache/` and `prom/`; `go build ./...` must pass; `go test ./...` must pass.
- [ ] **Step 5**: `make test-e2e` must pass, including the API-level tests (`e2e/metrics_test.go` `TestCorootIngestsContainerMetrics`, `e2e/express_test.go`), which exercise the full path agent → collector → TimeSeries → evaluator → World → API.
- [ ] **Step 6**: Measure on the dev cluster and write the numbers into this plan under "Results": page latency of `/overview/health`, `/app/<id>` for 1 h, 24 h, 7 d (10 requests each, median); ClickHouse CPU of the evaluator per cycle (`system.query_log`, `log_comment = 'coroot-evaluator'` — set it on evaluator queries); coroot RSS. Targets: page median ≤ 60 ms at 1 h on dev; evaluator ≤ 0.1 core.
- [ ] **Step 7: Commit** `git commit -m "refactor: pages read the world table; remove the metrics cache and the Go PromQL engine"`

---

### Task 8: Remove the external Prometheus integration

**Files:**
- Modify: `db/integrations.go` (`IntegrationPrometheus` and its use), `db/project.go` (`PrometheusConfig`), `config/` (global Prometheus flags/env), `api/forms/*` (Prometheus form), `api/api.go` (integration endpoints)
- Delete: `front/src/views/IntegrationPrometheus.vue` and its route/menu entries
- Modify: `docs/docs/` pages that describe connecting a Prometheus

- [ ] **Step 1**: `grep -rn "IntegrationPrometheus\|PrometheusConfig\|globalPrometheus" --include=*.go --include=*.vue --include=*.js .` → remove each use; the project's metrics source is always its ClickHouse.
- [ ] **Step 2**: `go build ./... && go test ./...` and `cd front && npm run test:unit && npm run build` pass.
- [ ] **Step 3**: `make test-e2e` passes.
- [ ] **Step 4: Commit** `git commit -m "refactor: drop the external Prometheus integration"`

---

### Task 9: (dropped)

Cluster mode is not removed; the opposite was done (D1). Nothing to do here; the number is kept so that references to Tasks 10–11 stay valid.

---

### Task 10: Housekeeping

**Files:**
- Modify: `clickhouse/space_manager.go` (include `world_%` and the TimeSeries inner tables `.inner_id.%` of `metrics` in the tables it can drop partitions from; today `metrics*` were never cleaned)
- Modify: `e2e/storage/metrics_bench_test.go` (benchmark `promql.Client.QueryRange` and `world.Store.Load` instead of the removed engine)
- Modify: `docs/superpowers/specs/2026-10-01-k8s-only-metric-labels-design.md` (link this plan: the labels now live in `timeSeriesTags`)

- [ ] **Step 1**: Space manager test (`clickhouse/space_manager_test.go`): the table filter selects `world_points`, `world_points_5m`, `world_points_1h`, `.inner_id.samples.*`; write the failing test, then change the `LIKE` list.
- [ ] **Step 2**: Bench update; run `COROOT_BENCH=1 make test-e2e TestBench` and paste the numbers into "Results".
- [ ] **Step 3: Commit** `git commit -m "chore: space manager and benchmarks for the new metrics tables"`

---

### Task 11: Peak values on long-range charts

Today a 24 h chart of memory or CPU shows the last value of each 15 min bucket, so a spike inside a bucket is invisible. The rollups already keep `Max` (Task 3). This task lets a query opt in.

**Files:**
- Modify: `timeseries/timeseries.go` (add `FillMax`, same shape as `FillAny`, keeping the largest non-NaN value)
- Modify: `world/store.go` (`aggMax`, `max(Value)` / `max(Max)` in `pointsSQL`, `Ms` array)
- Modify: `constructor/queries.go` (`.WithFillFunc(timeseries.FillMax)` on the queries where a peak matters: `container_memory_rss`, `container_memory_cache`, `container_resources_cpu_usage`'s rate, `node_memory_*` — review each with the owner of the chart before changing it)
- Test: `timeseries/timeseries_test.go`, `world/store_test.go`, `e2e/storage/world_test.go`

- [ ] **Step 1**: Failing test, then `FillMax`:

```go
func TestFillMax(t *testing.T) {
	// 15 s points at t = 0, 15, 30, 45, 60, 75
	data := []float32{1, 5, 2, NaN, 3, 4}
	ts := New(0, 4, 30) // points at 0, 30, 60, 90; each covers (t-30, t]
	FillMax(ts, 0, 15, data)
	assert.Equal(t, []float32{1, 5, 3, 4}, ts.data)
}
```

Run `go test ./timeseries -run TestFillMax` (FAIL: undefined), implement `FillMax` by copying `FillAny` and replacing `vv = v` with `if IsNaN(vv) || v > vv { vv = v }`, run again (PASS).
- [ ] **Step 2**: Extend `TestStoreSQLPicksTableByStep` (`AS Ms` present, `max(Max)` for rollups) and `TestPickValues` (`aggregateFor(timeseries.FillMax) == aggMax`); implement; pass.
- [ ] **Step 3**: Add `"max": timeseries.FillMax` to the map in `TestWorldStoreMatchesFillFuncs`; `make test-e2e TestWorldStoreMatchesFillFuncs` passes.
- [ ] **Step 4**: Switch the chosen queries; check the 24 h charts of an application page on the dev cluster before and after (screenshots in the PR).
- [ ] **Step 5: Commit** `git commit -m "feat: long-range memory and CPU charts show the peak of each bucket"`

---

### Task 12: Kubernetes only: remove Docker Compose, Docker Swarm and the systemd installer

Independent of Tasks 1–11; can be done first. After D1 (ClickHouse must be a cluster) the Compose and Swarm files start a single ClickHouse without Keeper, which Coroot now refuses, so they are broken anyway.

**Files:**
- Delete: `deploy/docker-compose.yaml`, `deploy/docker-swarm-stack.yaml`, `deploy/install.sh`
- Delete: `docs/docs/installation/docker.md`, `docs/docs/installation/docker-swarm.md`, `docs/docs/installation/ubuntu.md`, `docs/docs/installation/rhel.md`
- Modify: `docs/docs/quick-start/community-edition.md`, `docs/docs/quick-start/enterprise-edition.md` (keep only the Kubernetes tabs: Helm / operator, `kubernetes.md`, `k8s-operator.md`, `openshift.md`), `docs/docs/installation/requirements.md` and `architecture.md` (drop mentions of Docker/VM installs), `README.md`
- Check: `docs/sidebars.js` and `docs/docs/installation/_category_.yaml` (no links to deleted pages), `docs/docusaurus.config.js` redirects

**Interfaces:** none (deploy and docs only).

- [ ] **Step 1**: Delete the files listed above.
- [ ] **Step 2**: Find what still points at them and fix each hit:

```bash
grep -rnE "docker-compose|docker-swarm|docker compose|docker stack|deploy/install\.sh|installation/(docker|docker-swarm|ubuntu|rhel)" \
  --include='*.md' --include='*.js' --include='*.yaml' --include='*.yml' --include='*.json' --include='*.sh' --include=Makefile . \
  | grep -v node_modules | grep -v docs/superpowers
```

Expected after the fixes: no output. (`constructor/containers.go` mentions `/swarm/` container ids; that parsing is the subject of `docs/superpowers/specs/2026-10-01-k8s-only-metric-labels-design.md` and is not removed here.)
- [ ] **Step 3**: Build the docs: `cd docs && npm ci && npm run build`. Expected: success, no broken-link errors (Docusaurus fails the build on broken links).
- [ ] **Step 4**: `make dev` still comes up (it only uses `deploy/kind/`), `make test-e2e` passes.
- [ ] **Step 5: Commit** `git commit -m "chore: Kubernetes is the only supported deployment"`

The node-agent repo has its own `install.sh` (systemd install of the agent, linked from the quick-start pages). Removing it is a separate change in `coroot-node-agent`; the quick-start pages stop linking to it in Step 2.

---

## Results

(filled in by Task 7 Step 6 and Task 10 Step 2)

## Risks

- **Private preview.** ClickHouse documents the TimeSeries engine at schema version 7 while 26.9 creates version 4; a future version may require recreating the table. The world tables are plain MergeTree, so page history survives; only raw samples would need a copy (`INSERT INTO metrics_new (metric_name, tags, samples) SELECT ... FROM timeSeriesSamples/Tags(...)`). Task 1's suite is the gate for every ClickHouse bump.
- **Fixed cost per PromQL query (~14 CPU-ms).** Dashboards and the Prometheus API pay it per panel; a page with 20 custom panels costs ~0.3 CPU-s in ClickHouse. If that becomes visible, panels can be evaluated by the same evaluator (named queries) instead of on request.
- **Recording rules over backfill.** Building a World for 24 h at once is heavy; Task 6 computes them per hour during backfill.
- **Scale.** At 100k query-result series a 1 h page read is ~600 ms (measured). If pages get slow at that scale, add a 15 s in-memory cache of the last `Store.Load` result per window, shared by all requests.

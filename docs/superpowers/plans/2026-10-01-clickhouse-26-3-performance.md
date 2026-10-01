# ClickHouse 26.3 Performance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** keep log search, log histograms/facets and metrics dashboards fast as data grows, by moving to ClickHouse 26.3 and changing the three places where a measured change gives an order-of-magnitude speedup.

**Scope:** the coroot server: ClickHouse version, `ch/client.go` schema, the log query builders in `clickhouse/logs*.go`, the metrics write path in `collector/metrics.go` and the PromQL storage layer in `prom/clickhouse_querier.go`.

**Ground rules for this product:** single user, built from scratch, no backward compatibility. Schema changes go straight into the `CREATE TABLE` statements; no `ALTER` migrations, no dual-write, no reading old layouts. Existing ClickHouse data is dropped when this lands.

**Non-goals:**
- Traces. Measured: lists, facets, heatmaps and trace detail take 20-150 ms per day of data on the current schema, and nothing in 26.3 changes that by much (see "Measured and rejected").
- Profiles (low volume, not measured).
- The trace body columns plan (`2026-09-17-clickhouse-trace-body-columns.md`) is separate; its index choice should be revisited after Task 1 here (26.3 adds the `text` index type).

---

## Evidence

All numbers come from `scripts/chbench/` (reproduce with `scripts/chbench/run.sh`). Setup:

- ClickHouse 26.3.37.3 (26.3.38.2 is not on Docker Hub), 16 CPUs, 62 GB RAM.
- Synthetic data shaped like coroot's: 100 M logs and 50 M spans over one day, and 125 k metric series sampled every 15 s for 6 h (181 M samples).
- Queries copied from the coroot code (filters, the `timestampLimitCutoff` LIMIT trick, facets, the PromQL `Select`).
- Each query run 3 times with `use_query_cache=0, use_query_condition_cache=0`; best time reported.
- Every variant was checked to return the same rows as the current schema.

Limits of the evidence: one day of data, parts merged with `OPTIMIZE FINAL`, warm page cache, no concurrent ingest. Scanning queries grow linearly with retention; indexed and rollup queries hardly do. So on a 30-day retention the gains below are larger, not smaller.

| change | query | today | after |
| --- | --- | --- | --- |
| `text` index on `lowerUTF8(Body)` | log list, search for a rare token, 24 h | 1582 ms, 11.4 GiB read | 28 ms, 0.25 MiB |
| | log list, search for common tokens, 24 h | 872 ms, 6.3 GiB | 192 ms, 0.9 GiB |
| | log histogram with search, 24 h | 766 ms | 144 ms |
| minute rollup table | log histogram, 24 h | 210 ms | 5 ms |
| | facet Application, 24 h | 148 ms | 5 ms |
| | facet host.name (from the attribute maps), 24 h | 2056 ms | 5 ms |
| metrics split into series + samples | metric with 20 k series, 1 h | 659 ms, 5.3 GiB | 189 ms, 0.6 GiB |
| | same, 6 h | 1412 ms | 504 ms |
| | `namespace="ns-3"` matcher | 448 ms | 60 ms |
| | `namespace=~"..."` matcher | 876 ms | 86 ms |

Costs:
- The Body `text` index is 2.4x the size of `Body` itself (2.37 GiB vs 985 MiB per 100 M logs), and ingest with the index is about 2x slower in bulk.
- The rollup table is 5.8 MB per 100 M logs.
- The metrics split reduces storage (2.02 GiB → 1.22 GiB) and speeds up ingest about 4x.

### Measured and rejected

| idea | result | decision |
| --- | --- | --- |
| Time-first sort key, as ClickStack does: `(toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp)` | slower for coroot's queries: overview list 1 h 65 → 258 ms, one app 20 → 37 ms | keep `ServiceName` first |
| Bucketed `Map` serialization (`map_serialization_version='with_buckets'`) | host.name facet 2.1 s → 0.9 s, but every list reads whole maps and got slower (trace list 84 → 118 ms) | no; the rollup covers the facets |
| `text` index on `TraceId` | 46 → 35 ms, index 965 MiB vs 54 MiB for the current bloom filter | keep the bloom filter |
| `text` index on attribute `key=value` items | attribute filter 98 → 28 ms, 572 MiB of index | not now; revisit if attribute filters get slow |
| Lightweight projection on `otel_traces.TraceId` instead of the `trace_id_ts` MV | trace detail 28 → 19 ms; 200 traces at once only with `max_projection_rows_to_use_projection_index` raised, then equal to today | not worth the change |

---

## Global Constraints

- Do not git commit; the user drives that.
- No new README or summary documents.
- Tests first for Go.
- Each task ends with the measuring step for its own queries on a real ClickHouse 26.3, using `scripts/chbench` or a dev install loaded with `scripts/chseed`.

---

## File map

| File | Change |
| --- | --- |
| `deploy/docker-compose.yaml`, `deploy/docker-swarm-stack.yaml`, `deploy/kind/clickhouse.yaml`, dev scripts | ClickHouse image to 26.3 |
| `ch/client.go` | `otel_logs` Body index; `otel_logs_rollup` + MV; `metrics_samples` + `metrics_series` replace `metrics`; `ReplaceTables` list |
| `ch/client_test.go` | DDL assertions |
| `clickhouse/logs.go` | Message search via `hasAllTokens`; histogram from the rollup when allowed |
| `clickhouse/logs_facets.go` | facets from the rollup when allowed |
| `clickhouse/logs_rollup.go` (new), `clickhouse/logs_rollup_test.go` (new) | the "can this query use the rollup" decision and the rollup SQL |
| `clickhouse/logs_test.go` (new or existing) | search SQL assertions |
| `collector/metrics.go`, `collector/metrics_test.go` (new) | write samples and series |
| `prom/clickhouse_querier.go`, `prom/clickhouse_querier_test.go` (new) | `Select`, `LabelValues` against the split tables |
| `front/` help text for the logs search box | say that search matches whole words |

---

### Task 0: ClickHouse 26.3

- [ ] **Step 1.** Change the image in the three deploy files and anything under `scripts/dev` that pins a version to the newest 26.3 LTS tag on Docker Hub (26.3.37.3 at the time of writing; use 26.3.38.2 if it is published by then).
- [ ] **Step 2.** Drop the old data volume. With no compatibility requirement, start empty rather than upgrading in place.
- [ ] **Step 3.** Start the dev stack, confirm every statement in `ch/client.go` runs (verified for the current DDL on 26.3.37.3: all 16 tables create and replay cleanly) and that ingest and the UI work as before.
- [ ] **Step 4.** Know two default changes in 26.3:
  - `async_insert = 1` with `wait_for_async_insert = 1`. The collector already batches (10 000 rows or 2 s), so this mostly adds up to `async_insert_busy_timeout_ms` of insert latency. Set `async_insert=0` on the collector's connection if inserts get slower.
  - Insert deduplication is on by default: two byte-identical blocks are written once. Harmless for telemetry, but it explains a missing duplicate in a test.

---

### Task 1: Log search with a `text` index

Today `messageTokensExpr` (`clickhouse/logs.go`) turns every search token into `positionCaseInsensitiveUTF8(Body, @token) > 0`. No index can serve that, so every search reads every `Body` in the time range. The existing `idx_body tokenbf_v1(32768, 3, 0)` is unused.

**Behaviour change:** search matches whole tokens instead of substrings. `timeout` still finds `connection failed: timeout after 5s`, but `timeo` no longer does. The UI already splits the query into tokens on spaces and ASCII punctuation, which is the same rule the index uses (`splitByNonAlpha`), so most searches behave the same.

- [ ] **Step 1: Write the failing tests.**

```go
// clickhouse/logs_test.go
func TestMessageSearchUsesTokenIndex(t *testing.T) {
	q := LogQuery{Filters: []LogFilter{{Name: "Message", Op: "contains", Value: "Timeout DB-5432"}}}
	where, args := q.filters(nil)
	sql := strings.Join(where, " AND ")
	assert.Contains(t, sql, "hasAllTokens(lowerUTF8(Body), @tokens_0)")
	assert.NotContains(t, sql, "positionCaseInsensitiveUTF8")
	assert.Equal(t, []string{"timeout", "db", "5432"}, namedArg(t, args, "tokens_0"))
}

func TestMessageNotContains(t *testing.T) {
	q := LogQuery{Filters: []LogFilter{{Name: "Message", Op: "not contains", Value: "health check"}}}
	where, _ := q.filters(nil)
	assert.Contains(t, strings.Join(where, " AND "), "NOT hasAllTokens(lowerUTF8(Body), @not_tokens_0)")
}
```

`namedArg` is a small helper returning the value of a `clickhouse.Named` argument; write it in the test file. Fill the parts of `LogQuery` that `filters` needs (time range, `Ctx`) from an existing test or the struct's zero values.

- [ ] **Step 2: Run them (expect fail).** `go test ./clickhouse -run TestMessage -count=1`

- [ ] **Step 3: Implement.**
  - `ch/client.go`, `otel_logs`: replace `INDEX idx_body Body TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 1` with `INDEX idx_body lowerUTF8(Body) TYPE text(tokenizer = 'splitByNonAlpha') GRANULARITY 1`.
  - `clickhouse/logs.go`: `messageTokensExpr` lowercases the tokens (`strings.ToLower`), passes them as one array argument and returns `hasAllTokens(lowerUTF8(Body), @<prefix>_0)`. The positive filters keep pooling their tokens into one call; each `not contains` filter stays its own `NOT (...)`.
  - The index expression and the query expression must be the same text (`lowerUTF8(Body)`); a mismatch silently falls back to a full scan.
  - Update the search box help text in `front/` (find it from `Logs.vue` / `logQuickFilters.js`) to say that it matches whole words.

- [ ] **Step 4: Run the package.** `go test ./clickhouse -count=1`

- [ ] **Step 5: Measure.** The benchmark used `lower(Body)`. Before relying on `lowerUTF8`, confirm the index is used:

```sql
EXPLAIN indexes = 1
SELECT count() FROM otel_logs WHERE hasAllTokens(lowerUTF8(Body), ['timeout'])
```

  - Expected: `idx_body` in the plan with most granules dropped.
  - If `lowerUTF8` is not served, use `lower(Body)` in both places. It only lowercases ASCII, so non-ASCII text becomes case-sensitive.
  - Then run Q04, Q05 and Q07 from `scripts/chbench/logs_queries.py` and compare with the evidence table.
  - Record the index size (`system.data_skipping_indices`) against `Body` (`system.parts_columns`).

---

### Task 2: Minute rollup for log histograms and facets

The logs histogram (`GetLogsHistogram`) and every facet (`facetCountSQL`) aggregate raw `otel_logs` rows over the whole window. A pre-aggregated per-minute table answers them from a tiny table, the same idea coroot already uses for traces (`otel_traces_histogram`).

**When the rollup can answer** (everything else keeps the raw query):
- No `Message` filter, no `TraceId` filter, and no attribute filter except `host.name`.
- Filters only on Severity, `service.name`/ServiceName (including `~` regex), Source, Namespace, Application or `host.name`.
- Histogram step of at least 60 s.
- The window is not in live-tail mode (`Since` unset).

Times are rounded to the minute, as the traces histogram already is.

- [ ] **Step 1: Write the failing tests** in `clickhouse/logs_rollup_test.go`:
  - `TestLogRollupEligible` (table-driven): an eligible query; one with a Message filter; one with an attribute filter other than `host.name`; one with a TraceId filter; one with a 15 s step; one with `Since` set. Only the first is eligible.
  - `TestLogRollupHistogramSQL`: the SQL reads `@@table_otel_logs_rollup@@`, sums `Count`, and groups by `multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1)` and `toStartOfInterval(Minute, ...)`.
  - `TestLogRollupFacetSQL`, one case per facet in `facetCountSQL`: the same shape over the rollup columns with `sum(Count)`.

- [ ] **Step 2: Run them (expect fail).** `go test ./clickhouse -run TestLogRollup -count=1`

- [ ] **Step 3: Implement.**
  - `ch/client.go`: the table and its MV, after `otel_logs`.

```sql
CREATE TABLE IF NOT EXISTS otel_logs_rollup @on_cluster (
    Minute DateTime CODEC(Delta, ZSTD(1)),
    ServiceName LowCardinality(String) CODEC(ZSTD(1)),
    Namespace LowCardinality(String) CODEC(ZSTD(1)),
    Application LowCardinality(String) CODEC(ZSTD(1)),
    SeverityNumber Int32 CODEC(ZSTD(1)),
    Host LowCardinality(String) CODEC(ZSTD(1)),
    Count UInt64 CODEC(ZSTD(1))
) ENGINE @summing_merge_tree
PARTITION BY toDate(Minute)
ORDER BY (ServiceName, Minute, SeverityNumber, Namespace, Application, Host)
TTL Minute + toIntervalSecond(@ttl_logs)
SETTINGS ttl_only_drop_parts = 1

CREATE MATERIALIZED VIEW IF NOT EXISTS otel_logs_rollup_mv @on_cluster TO otel_logs_rollup AS
SELECT toStartOfMinute(Timestamp) AS Minute, ServiceName, Namespace, Application, SeverityNumber,
       if(LogAttributes['host.name'] != '', LogAttributes['host.name'], ResourceAttributes['host.name']) AS Host,
       count() AS Count
FROM otel_logs GROUP BY Minute, ServiceName, Namespace, Application, SeverityNumber, Host
```

  - Add `otel_logs_rollup` to the `ReplaceTables` list and a `_distributed` variant to `distributedTables`.
  - `clickhouse/logs_rollup.go`: an `eligible(LogQuery) bool` function, and a `rollupFilters` that maps the eligible filters onto rollup columns:
    - Severity → `SeverityNumber BETWEEN`;
    - `host.name` → `Host`;
    - Source → `startsWith(ServiceName, '/')`;
    - time → `Minute BETWEEN toStartOfMinute(@from) AND @to`.
  - `GetLogsHistogram` and `GetLogFacetCounts` call the rollup variant when eligible, otherwise the current SQL. The "Cluster" facet becomes `sum(Count)`.
  - Read results with `sum(Count)`, never `count()`: a SummingMergeTree has unmerged duplicates until merges run.

- [ ] **Step 4: Run the package.** `go test ./clickhouse -count=1`

- [ ] **Step 5: Verify the counts match.** On a dev install with a few hours of logs, for the histogram and every facet over a minute-aligned window, the rollup result must equal the raw result. A difference means the MV and the raw query disagree on a column definition (Namespace, Application, Host or severity bucket).

- [ ] **Step 6: Measure** Q06, Q08 and Q09 from `scripts/chbench` against the rollup.

---

### Task 3: Metrics as series + samples

Today every sample row in `metrics` carries the full `Labels` map, and `clickhouseQuerier.Select` groups by that map. A label matcher therefore reads the labels of every sample of the metric in the window. Splitting stores labels once per series, and matchers are resolved on the small series table first.

- [ ] **Step 1: Write the failing tests.**

  `prom/clickhouse_querier_test.go`, against the SQL builders (factor the SQL construction out of `Select` and `LabelValues` into functions that return strings, so they can be tested without a server):
  - `TestSelectSQLNoLabelMatchers`: `{__name__="m"}` reads `@@table_metrics_samples@@` with `MetricName = 'm'` and the time range, groups by `MetricHash`, and joins `@@table_metrics_series@@` for the labels; no `Labels[` in the samples subquery.
  - `TestSelectSQLLabelMatchers`: `{__name__="m", namespace="a", pod=~"x.*"}` puts both label conditions only in the series subqueries, and the samples subquery filters `MetricHash IN (SELECT MetricHash FROM @@table_metrics_series@@ WHERE ...)`.
  - `TestLabelValuesSQL`: label values come from `@@table_metrics_series@@` with `LastSeen >= ...`, not from samples.

  `collector/metrics_test.go`:
  - `TestMetricsBatchWritesSeriesOncePerBatch`: two samples of one series and one sample of another produce 3 sample rows and 2 series rows; the sample columns have no `Labels`.

- [ ] **Step 2: Run them (expect fail).** `go test ./prom ./collector -run 'TestSelectSQL|TestLabelValuesSQL|TestMetricsBatch' -count=1`

- [ ] **Step 3: Implement the schema** (`ch/client.go`), replacing `metrics`:

```sql
CREATE TABLE IF NOT EXISTS metrics_samples @on_cluster (
    MetricName LowCardinality(String) CODEC(ZSTD(1)),
    MetricHash UInt64 CODEC(ZSTD(1)),
    Timestamp DateTime('UTC') CODEC(DoubleDelta, ZSTD(1)),
    Value Float64 CODEC(Gorilla, ZSTD(1))
) ENGINE @merge_tree
PARTITION BY toDate(Timestamp)
ORDER BY (MetricName, MetricHash, Timestamp)
TTL Timestamp + toIntervalSecond(@ttl_metrics)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1

CREATE TABLE IF NOT EXISTS metrics_series @on_cluster (
    MetricName LowCardinality(String) CODEC(ZSTD(1)),
    MetricHash UInt64 CODEC(ZSTD(1)),
    Labels Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    LastSeen DateTime('UTC') CODEC(Delta, ZSTD(1))
) ENGINE @replacing_merge_tree(LastSeen)
ORDER BY (MetricName, MetricHash)
TTL LastSeen + toIntervalSecond(@ttl_metrics)
```

  - `Timestamp` becomes `DateTime`; the collector already truncates samples to seconds.
  - Update `ReplaceTables` and `distributedTables`: shard both tables by `MetricHash`, so a series and its samples land on the same shard.
  - `@replacing_merge_tree(LastSeen)` needs the placeholder replacement in `ch/client.go` to accept a version argument; check how `@replacing_merge_tree` is expanded and extend it.

- [ ] **Step 4: Implement the write path** (`collector/metrics.go`):
  - `MetricsBatch` gets sample columns (MetricName, MetricHash, Timestamp, Value) and series columns (MetricName, MetricHash, Labels, LastSeen), plus a per-batch `map[uint64]int` index of series already added.
  - `Add` appends one sample row per sample, and one series row per distinct `MetricHash` per batch, with `LastSeen` = the newest sample time in the batch.
  - `save()` inserts into `@@table_metrics_series@@` and `@@table_metrics_samples@@`. Insert series first, so a query never sees samples without labels.
  - The ReplacingMergeTree collapses repeats across batches; queries must not depend on it being merged (next step).

- [ ] **Step 5: Implement the read path** (`prom/clickhouse_querier.go`).

`Select`:

```sql
SELECT s.MetricName, s.Labels, d.Timestamps, d.Values
FROM (
    SELECT MetricHash, groupArray(toUnixTimestamp(Timestamp)) AS Timestamps, groupArray(Value) AS Values
    FROM @@table_metrics_samples@@
    WHERE MetricName = '<name>' AND Timestamp >= toDateTime(<mint>) AND Timestamp <= toDateTime(<maxt>)
      [AND MetricHash IN (SELECT MetricHash FROM @@table_metrics_series@@ WHERE MetricName = '<name>' AND <label conditions>)]
    GROUP BY MetricHash
) AS d
INNER JOIN (
    SELECT MetricHash, any(MetricName) AS MetricName, any(Labels) AS Labels
    FROM @@table_metrics_series@@
    WHERE MetricName = '<name>' [AND <label conditions>]
    GROUP BY MetricHash
) AS s USING MetricHash
```

  - The inner aliases must not shadow the columns used in `WHERE`. `any(MetricName) AS MetricName` inside the same SELECT fails with `ILLEGAL_AGGREGATION`. Use distinct aliases (`mn`, `lbl`) and rename them in the outer SELECT.
  - `GROUP BY MetricHash` with `any()` makes the result independent of whether the ReplacingMergeTree has merged.
  - A selector without `__name__` (`{job="x"}`) has no `MetricName` condition. It still works, but scans all metrics in the time range, as today.
  - The row-to-series conversion in `OnResult` stays as is.
  - `LabelValues` reads `@@table_metrics_series@@` with `LastSeen >= toDateTime(<from>)` and the matcher conditions. `__name__` values become `SELECT DISTINCT MetricName`.

- [ ] **Step 6: Run the packages.** `go test ./prom ./collector -count=1`

- [ ] **Step 7: Verify end to end.**
  - Load metrics with the node agent for a while.
  - For a handful of coroot's own dashboard PromQL queries (take them from the app's query list), compare the series and values against a run of the current build on the same input. Simplest: keep a copy of the old `metrics` table loaded by the old collector on the side for the comparison.
  - Then run M1-M6 from `scripts/chbench`.

---

## Self-review

1. **Every change is backed by a measured speedup of 5x or more on coroot's own query shapes**, with the cost stated (index size, ingest time). Ideas measured as small or harmful are listed with their numbers instead of being silently dropped. That matters because two of them are the published ClickStack recommendations.
2. **Correctness is checked, not assumed.** The benchmark compared row counts between variants, and each task ends with a raw-vs-new comparison: search hits, rollup counts against raw counts, PromQL series and values against the old table.
3. **The one user-visible behaviour change is named.** Search matches whole tokens, and the UI text says so.
4. **The rollup is optional per query.** Anything it cannot answer exactly keeps the raw path, using the same rule the traces histogram MV already uses (`useTracesHistogram`).
5. **Known traps are written into the steps:** the index expression must match the query expression; the alias clash in the metrics join; `sum(Count)` versus `count()` on a SummingMergeTree; the `async_insert` default in 26.3.
6. **What the evidence does not cover is stated:** one day of data, warm cache, no concurrent ingest, profiles and traces left out. Each task re-measures on real ingest before calling it done.

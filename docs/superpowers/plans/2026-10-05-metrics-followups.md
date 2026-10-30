# Metrics follow-ups (future work)

> **Status: ❌ NOT IMPLEMENTED** (verified against the code and git history on 2026-10-11). None of the items is done, except item 1 (done) and items 2 and 4, closed by decisions (2026-10-29; in 4 the `lastPerQuery` fix and the query marking are done). Original note: not scheduled.

These are the gaps left after `2026-10-05-clickhouse-promql-metrics.md` (all 12 tasks done). Each item says what is wrong, the evidence, and the first thing to try. Numbers are from the dev cluster (2 shards, 2026-10-05).

## 1. `timestamp()` is not supported by ClickHouse PromQL — done 2026-10-29

- **Problem:** `mongo_rs_last_applied_timestamp_ms` (`constructor/queries.go`) uses `timestamp()`; ClickHouse PromQL rejects it, so the query returns nothing. It is 1 of 393 queries.
- **Evidence:** `e2e/storage/promql_compat_test.go` (`TestPromQLCompat`), the audit run: 94 queries identical, 298 empty, 1 `timestamp()` error.
- **Options:** (a) rewrite the query without `timestamp()` (check what the Mongo replication-lag inspection really needs; maybe `time() - metric/1000` on a gauge), (b) compute it in Go from the raw series, (c) wait for ClickHouse support and re-run the compat test on each upgrade.
- **Done when:** the Mongo replica-set lag shows on a dev cluster with a Mongo, and `TestPromQLCompat` has a case for it.
- **Done 2026-10-29 (not committed):** option (a). The query is `time() - mongo_rs_last_applied_timestamp_ms/1000`. The auditor uses it only as the difference between a secondary and the primary of a replica set (`mongoInstanceLag`, `calcMongoPrimaryBaseline`), and in that difference `time()` cancels out: the lag is the difference of the applied optimes, as before (before, the scrape times of the two instances were in it too). `TestPromQLCompat` ("the Mongo replication lag query") runs the constructor's query on a primary and a secondary 40 s behind and checks the difference is 40 at every step; it failed with the old query. Not checked on a real Mongo: the dev cluster has none.

## 2. Data lag of 60–75 s on pages (was ~45 s)

- **Problem:** the pipeline waits `lag=2` steps, then recording rules wait `ruleLookahead=2` steps, on a 15 s `world.Period`.
- **Evidence:** `world/rules.go`, `world/run.go`; lookahead 3 with a 30 s period gave 90 s lag and two e2e failures, so those values are a trade-off, not a free knob.
- **Options:** (a) evaluate rules incrementally on the points of the last cycle instead of re-reading `ruleHistory` and waiting for lookahead, (b) make `lag` adaptive: shrink it while the agents' remote-write latency is low (measure `now - max(sample time)` per project), (c) accept it and say so in the docs.
- **Decided 2026-10-29: option (c).** The delay is accepted and documented in `docs/docs/configuration/metrics.md` ("Data delay"). Options (a) and (b) are not planned; reopen only if users ask for fresher pages.

## 3. Page latency under load (target median ≤ 60 ms at 1 h)

- **Problem:** quiet cluster: 33–69 ms at 1 h; with the evaluator running and e2e load: median 142–169 ms, p90 up to 823 ms (`app` 1 h).
- **Evidence:** measurements in the plan's "Results"; the window cache is 5 s (`world/store.go`, `windowTTL`) and one page loads every query of the window.
- **Options:** (a) load only the queries a page needs instead of the whole window, (b) a longer window cache with invalidation on a new evaluation cycle, (c) reduce rows read by pruning on `Query` (it is the second sort-key part after the hour), (d) re-measure on a quiet cluster and under a realistic number of series before optimizing: the dev data set is small.
- **Done when:** the measurement script of Task 7 Step 6 gives a median ≤ 60 ms at 1 h with the evaluator running.
- **Measured 2026-10-29** on dev with 7 days of world data (today's copied to the 6 days before), quiet cluster, the evaluator running, 10 requests per page; "cold" = the page's window not in the 5 s window cache, "warm" = the same request right after:

  | page | cold median | cold p90 | warm median |
  |---|---|---|---|
  | `/health` (the network floor) | 13 ms | 14 ms | 12 ms |
  | overview/applications 1h / 24h / 7d | 617 ms / 2.1 s / 13.2 s | 623 ms / 2.2 s / 13.4 s | 27 / 31 ms / 13.2 s |
  | overview/map 1h | 254 ms | 635 ms | 25 ms |
  | app express-demo 1h / 24h / 7d | 39 ms (the window was cached by the page before) / 2.1 s / 10.1 s | 49 ms / 2.2 s / 13.5 s | 36 / 39 ms / 3.4 s |

  - **Where the time goes:** building the World with the same `Store`, `constructor` and `auditor` as a page (a throwaway test with the constructor's stage profile): the `query` stage (reading the windows) is 646 ms of 696 ms at 1 h, 2.2 s of 2.3 s at 24 h, 13.8 s of 13.9 s at 7 d; `auditor.Audit` is 2 ms. ClickHouse itself answers in 107 ms at 1 h and ≤ 0.9 s per query at 7 d (`system.query_log`): **most of the time is the Go side decoding the rows**. The CPU profile at 7 d: 77 % in clickhouse-go's `column.(*Array).scanSlice` (arrays scanned into slices through `reflect`, with an allocation per element), the rest the GC.
  - **Why 24 h and 7 d read so much:** a page also reads the SLI queries at the raw step (15 s) from `min(from, to - MaxAlertRuleWindow)` (`constructor.queryCache`, `_raw` queries), and `Store.load` reads every query of a window, not only the ones asked for. So a 7 d page reads the raw points of all 113 queries for 7 days (57.7 M points, 11 s in ClickHouse when not cached) while only the 3 SLI queries need them (7.1 M points, 12 %).
  - **Fixes, by expected effect:**
    1. A window reads only the queries asked for it (`Query IN (...)` in `pointsSQL`, the set of queries part of the window key): the raw window of a 24 h / 7 d page shrinks ~8x. Option (a) above.
    2. Decode the window with ch-go (columnar, no `reflect`) instead of clickhouse-go's `Scan` into `[]uint32` / `[]float32`: the cost at every range, the 1 h page included (~0.8 M points, ~0.5 s of decoding).
    3. Then re-measure; the 60 ms target at 1 h needs 2 (ClickHouse alone is ~100 ms cold at 1 h, so a cold 1 h page stays above 60 ms; warm pages are ~30 ms already).
- **Done 2026-10-29 (not committed):** both fixes.
  - **Only the queries asked for:** `constructor.Cache` has `QueryRanges(ctx, map[name]QueryRequest)` instead of `QueryRange`: `queryCache` hands every request of a run at once, and `world.Store.QueryRanges` reads each window once (the windows in parallel), a window of at most `maxFilteredQueries` (50) queries with `Query IN (...)` in `pointsSQL` and `seriesSQL`, a larger one every query. The store keeps nothing between calls: the 5 s window cache (`windowTTL`, `sync.Once` per window, shared by concurrent runs) is gone, as is a first version of this fix that told the store the queries in advance (`QueryPlanner`, in `6372ee0`). Tests: `TestPointsSQLReadsOnlyTheListedQueries`, and `TestWorldStoreMatchesFillFuncs` ("a window of a few queries reads those, as a window of every query does", a query with a quote and a backslash in the IN list; "the windows of one read are read together").
  - **Decoding:** a window is read as one row per point instead of an array per series. Measured on a 6 h raw window (2.3 M points): arrays 632 ms, one row per point 254-325 ms. Tried and dropped: ch-go (ClickHouse 26.9 rejects its distributed reads, "Sending a distributed query with unknown (zero) client version", the same as D1 of the PromQL plan; ch-go 0.74 does not build with clickhouse-go 2.17), arrays packed into strings with `reinterpretAsFixedString` (5x faster but loses trailing zero bytes: 2741 series came back with a wrong length).
  - **Result on dev**, same method as above:

    | page | cold median before | after |
    |---|---|---|
    | overview/applications 1h / 24h / 7d | 617 ms / 2.1 s / 13.2 s | 159 / 193 / 857 ms |
    | overview/map 1h | 254 ms | 25 ms |
    | app express-demo 24h / 7d | 2.1 s / 10.1 s | 40 / 81 ms |

    The World of a page alone (LoadWorld + Audit, a throwaway test): 1 h 696 → ~180 ms, 24 h 2.27 s → ~215 ms, 7 d 13.9 s → ~830 ms. Warm pages stay ~25-40 ms; a cold 1 h page is still above the 60 ms target (ClickHouse alone ~100 ms).
  - **Without the window cache (2026-10-30, decided for simplicity):** the numbers above have the 5 s window cache, shared by the requests of one 15 s step (`overview/map` and the `app` pages reused the window `overview/applications` had just loaded). With `QueryRanges` every request reads its windows itself:

    | page | first request, cache → none | same request right after, cache → none |
    |---|---|---|
    | overview/applications 1h / 24h / 7d | 159 / 193 / 857 → 148 / 185 / 787 ms | 25 / 31 / 74 → 131 / 179 / 793 ms |
    | overview/map 1h | 25 → 133 ms | 24 → 134 ms |
    | app express-demo 1h / 24h / 7d | 37 / 40 / 81 → 144 / 189 / 795 ms | 36 / 39 / 81 → 142 / 186 / 797 ms |

    A page opened on its own is as fast as before; moving between pages of the same range within a few seconds costs ~0.1 s more (~0.7 s at 7 d). Accepted: a store with no state is worth it. If it matters later, the cheapest way back is a 5 s cache of a window read keyed by (window, queries), concurrent reads of one key joined; no planning is needed for it.
  - **Pages without raw SLIs (2026-10-30):** a page read the SLI queries only at 15 s from `min(from, to - MaxAlertRuleWindow)` (6 h on dev, 7 d on a 7 d page) and averaged them to its step in Go (`aggregateSLI`); only the burn rates of incidents need them at 15 s. Now every World reads the SLIs at its own step with `FillAvg` (keys `<name>_sli`), and only `OptionLoadRawSLIs` adds the raw ones (`<name>_raw`): the watchers (incidents) and RCA (its metrics go to Coroot Cloud as before) set it, the pages, the collector, stats and the recording rules do not (`OptionDoNotLoadRawSLIs` is gone). `aggregateSLI` / `aggregateHistogram` are gone; the "no data" of the availability check reads `TotalRequests` (the latency check already read the page-step histogram). A page now has one window. Differences: the page-step SLI buckets are right-closed like every other query, `(t - step, t]`, where `aggregateSLI` made them left-closed `[t, t + step)`: the SLI charts move by one step; the check's "no data" looks at the last 3 page steps instead of the last 45 s. Tests: `TestLoadSLIs`, `TestQueryCacheReadsRawSLIsOnlyWhenAsked`. On dev, same method (median of the first request, before → after): overview/applications 1h / 24h / 7d 148 / 185 / 787 → 142 / 170 / 189 ms, app express-demo 1h / 24h / 7d 144 / 189 / 795 → 142 / 169 / 190 ms. The SLO checks of the app page are the same at 1 h, 24 h and 7 d.

## 4. Evaluator cost in ClickHouse (~0.2 core, target 0.1) — closed 2026-10-29

- **Problem:** 1982 `prometheusQueryRange` queries and 64 CPU-s in 5 minutes; 97 queries are evaluated per 15 s cycle, each one a separate call.
- **Options:** (a) batch queries that share the same selector (one call per metric, then split by the query's label filter), (b) evaluate queries whose metric has not received new samples less often, (c) set `log_comment = 'coroot-evaluator'` on the evaluator's queries (the plan asked for it; check it is there) so the cost is visible in `system.query_log`, (d) a cheaper cycle for 296 skipped queries (already skipped by the metric-name check; keep the check cached).
- **`lastPerQuery` reads the whole table every cycle:** `SELECT Query, toUInt32(max(Timestamp)) FROM world_points GROUP BY Query` (`world/backend.go`, goes to `world_points_distributed`, so both shards) has no time filter: it scans `Query` and `Timestamp` of every partition in the retention window, every 15 s, and `recordingRules` calls it a second time in the same cycle. Not measured yet: first read its cost in `system.query_log`. A fix that keeps the result: add `WHERE toStartOfHour(Timestamp) >= toStartOfHour(now() - 24h)` (prunes by the sort key; same result because `from` is never earlier than `to - Backfill`, 24 h, so older rows only ever mean "backfill"). Also call it once per cycle and pass the map to both `cycle` and `recordingRules`. Test: `TestWorldEvaluator` / `TestWorldRecordingRules` (no duplicates after a restart) must stay green; add a case where the last point of a query is older than `Backfill`.
- **Done when:** `system.query_log` shows ≤ 0.1 core for the evaluator over 5 minutes on the dev cluster.
- **Closed 2026-10-29 by decision (code in `7151b75`):** (c) and the `lastPerQuery` fix are done; the 0.1 core target is dropped, (a) is measured not worth it, (b) and (d) stay as ideas. Reopen if the cost matters, e.g. many projects on one ClickHouse (the cost grows with the number of projects).
  - **(c) done:** every query of the evaluator has `log_comment = 'coroot-evaluator'`: the reads through `ch.WithSettings` (it keeps the settings added before; clickhouse-go's own `WithSettings` replaces them, so `TimeSeriesContext` dropped any comment), the writes through `insertSettings` (`world/backend.go`). Checked by `TestWorldEvaluator` ("the evaluator's queries are marked in the query log"). Query to measure: `SELECT count(), sum(ProfileEvents['OSCPUVirtualTimeMicroseconds'])/1e6 FROM clusterAllReplicas(coroot, system.query_log) WHERE log_comment = 'coroot-evaluator' AND type = 'QueryFinish' AND event_time > now() - INTERVAL 5 MINUTE`.
  - **Bug of (c), found and fixed 2026-10-29 (fix not committed):** `ch.WithSettings` as committed in `7151b75` put clickhouse-go's options into the evaluator's context; clickhouse-go writes `max_execution_time` into the settings of the context it derives from, and the 8 parallel PromQL queries of a cycle did so on one map: `fatal error: concurrent map writes`, 42 Coroot crashes on dev in an hour (and the Tilt port-forward dropping during e2e runs). Now `ch.WithSettings` only records the settings in the context, and `ch.QueryContext` makes clickhouse-go's options per query (`clickhouse.Client.Query` and `QueryRow` call it). Test: `TestSettingsAreSafeForConcurrentQueries` (`go test -race ./ch`; it reported the race and the fatal error on the committed code).
  - **`lastPerQuery` done:** it reads only `Timestamp >= now - backfill - 1h` and runs once per cycle (every 15 s per project, on the primary); `EvaluateProject` passes its result to `cycle` and `recordingRules`. Measured on dev with 7 days of world data (today's copied to the 6 days before, as with the default 7 d `METRICS_TTL`):
    - one call, directly: 49 → 17 ms, 57 → 9 M rows, 279 → 57 CPU-ms over the shards;
    - live, 5 minutes: 40 → 20 calls, 12.0 → 1.8 CPU-s, 2.28 G → 0.33 G rows read.
  - **The evaluator after it:** ~55 CPU-s per 5 min (~0.18 core), of which `prometheusQueryRange` ~45 CPU-s: ~25 queries of connections and traffic (`sum by(app_id, destination, ...) (rate(container_net_*...))`) at ~70-80 ms each (~60 %), ~80 simple queries of cluster state and metadata (`kube_pod_info`, `kube_*_annotations`, `container_info`, ...) at ~14 ms each, the fixed cost of a PromQL query.
  - **(a) measured, not worth it:** the 105 evaluated expressions read 103 different metrics (2 metrics are shared), so there is nothing to merge by selector; 20 different queries in one `UNION ALL` cost 916 CPU-ms against 1017 ms as 20 calls (~10 %): the fixed cost is inside `prometheusQueryRange`, not per call.
  - **(b) if the cost matters:** evaluate the slowly changing metadata queries (`kube_*_info`, `*_labels`, `*_annotations`, `container_info`) every minute instead of every 15 s, filling the steps in between: ~4x less for ~80 queries, ~25-30 % of the evaluator. The connection queries need the schema change discussed on 2026-10-29 (fact and dimension tables instead of PromQL), not tuning.
  - **Not the evaluator, seen while measuring:** the pages' window reads (`Store.read`) take ~13 CPU-s per 5 min, and `Store.LastEvaluated` scans a day of `world_points` every 5 s (`lastEvaluatedTTL`, was `windowTTL`) per replica.

## 5. Large PromQL reads over the TimeSeries table

- **Problem:** a selector over 20 k series returning 10 k of them takes 0.96 s for 1 h and 5.1 s for 6 h (`TestBenchMetrics`).
- **Context:** the evaluator and the PromQL HTTP API (`api/prometheus.go`, dashboards) use this path; pages do not.
- **Options:** check `EXPLAIN` of `prometheusQueryRange` for index use on `timeSeriesTags`, see whether label matchers are pushed down, try a narrower time pruning of the data table, or steer such dashboards to the `world_*` rollups when the query is one of the evaluated ones.
- **Done when:** the bench numbers are in the plan's "Results" with a before/after.

## 6. Smaller items

- **`FillMax` scope:** only `container_cpu_usage`, `container_memory_rss`, `container_memory_cache` use it. Review per chart whether other gauges (disk usage, connection counts, queue depths) should show the peak. No screenshots were taken before/after.
- **Remote Coroot (multi-cluster):** `remoteCoroot.metricResolution` and the Prometheus config for remote projects were removed; metrics for a member project now come only from its ClickHouse. Verify on a real two-cluster setup that member projects still render.
- **Old `prometheus` column:** `project.prometheus` stays in the schema and is unused; drop it with a migration when convenient.
- **Windows agent docs:** `docs/docs/installation/windows.md` and the node-agent `install.sh` were left alone; decide with the k8s-only direction (`specs/2026-10-01-k8s-only-metric-labels-design.md`).

## Not metrics, deferred from the audit

Agent-side items noted earlier and never started: ring buffer `NO_WAKEUP`, the byte-by-byte copy in the HTTP/1 parser, socket-layer capture, TLS loss; and the `space_manager` cleanup of old metrics tables is done only for `world_%` (inner TimeSeries tables rely on their TTL).

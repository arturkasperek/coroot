# Metrics follow-ups (future work)

Status: not scheduled. Nothing here is implemented.

These are the gaps left after `2026-10-05-clickhouse-promql-metrics.md` (all 12 tasks done). Each item says what is wrong, the evidence, and the first thing to try. Numbers are from the dev cluster (2 shards, 2026-10-05).

## 1. `timestamp()` is not supported by ClickHouse PromQL

- **Problem:** `mongo_rs_last_applied_timestamp_ms` (`constructor/queries.go`) uses `timestamp()`; ClickHouse PromQL rejects it, so the query returns nothing. It is 1 of 393 queries.
- **Evidence:** `e2e/storage/promql_compat_test.go` (`TestPromQLCompat`), the audit run: 94 queries identical, 298 empty, 1 `timestamp()` error.
- **Options:** (a) rewrite the query without `timestamp()` (check what the Mongo replication-lag inspection really needs; maybe `time() - metric/1000` on a gauge), (b) compute it in Go from the raw series, (c) wait for ClickHouse support and re-run the compat test on each upgrade.
- **Done when:** the Mongo replica-set lag shows on a dev cluster with a Mongo, and `TestPromQLCompat` has a case for it.

## 2. Data lag of 60–75 s on pages (was ~45 s)

- **Problem:** the pipeline waits `lag=2` steps, then recording rules wait `ruleLookahead=2` steps, on a 15 s `world.Period`.
- **Evidence:** `world/rules.go`, `world/run.go`; lookahead 3 with a 30 s period gave 90 s lag and two e2e failures, so those values are a trade-off, not a free knob.
- **Options:** (a) evaluate rules incrementally on the points of the last cycle instead of re-reading `ruleHistory` and waiting for lookahead, (b) make `lag` adaptive: shrink it while the agents' remote-write latency is low (measure `now - max(sample time)` per project), (c) accept it and say so in the docs.
- **Done when:** a data point reaches a page within ~45 s at the dev scrape interval, and `TestCorootIngestsOtelTraces` / external-service classification tests stay green.

## 3. Page latency under load (target median ≤ 60 ms at 1 h)

- **Problem:** quiet cluster: 33–69 ms at 1 h; with the evaluator running and e2e load: median 142–169 ms, p90 up to 823 ms (`app` 1 h).
- **Evidence:** measurements in the plan's "Results"; the window cache is 5 s (`world/store.go`, `windowTTL`) and one page loads every query of the window.
- **Options:** (a) load only the queries a page needs instead of the whole window, (b) a longer window cache with invalidation on a new evaluation cycle, (c) reduce rows read by pruning on `Query` (it is the second sort-key part after the hour), (d) re-measure on a quiet cluster and under a realistic number of series before optimizing: the dev data set is small.
- **Done when:** the measurement script of Task 7 Step 6 gives a median ≤ 60 ms at 1 h with the evaluator running.

## 4. Evaluator cost in ClickHouse (~0.2 core, target 0.1)

- **Problem:** 1982 `prometheusQueryRange` queries and 64 CPU-s in 5 minutes; 97 queries are evaluated per 15 s cycle, each one a separate call.
- **Options:** (a) batch queries that share the same selector (one call per metric, then split by the query's label filter), (b) evaluate queries whose metric has not received new samples less often, (c) set `log_comment = 'coroot-evaluator'` on the evaluator's queries (the plan asked for it; check it is there) so the cost is visible in `system.query_log`, (d) a cheaper cycle for 296 skipped queries (already skipped by the metric-name check; keep the check cached).
- **`lastPerQuery` reads the whole table every cycle:** `SELECT Query, toUInt32(max(Timestamp)) FROM world_points GROUP BY Query` (`world/backend.go`, goes to `world_points_distributed`, so both shards) has no time filter: it scans `Query` and `Timestamp` of every partition in the retention window, every 15 s, and `recordingRules` calls it a second time in the same cycle. Not measured yet: first read its cost in `system.query_log`. A fix that keeps the result: add `WHERE toStartOfHour(Timestamp) >= toStartOfHour(now() - 24h)` (prunes by the sort key; same result because `from` is never earlier than `to - Backfill`, 24 h, so older rows only ever mean "backfill"). Also call it once per cycle and pass the map to both `cycle` and `recordingRules`. Test: `TestWorldEvaluator` / `TestWorldRecordingRules` (no duplicates after a restart) must stay green; add a case where the last point of a query is older than `Backfill`.
- **Done when:** `system.query_log` shows ≤ 0.1 core for the evaluator over 5 minutes on the dev cluster.

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

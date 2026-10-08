# Sharded metrics after ClickHouse PromQL-over-Distributed (future work)

Status: not scheduled, **blocked on an upstream ClickHouse change that is not released**. Nothing here is implemented.

## Why this plan exists

Today the `metrics` TimeSeries table lives in a separate one-shard cluster (`coroot_metrics`, every node a replica) because ClickHouse 26.9.10.4 cannot evaluate PromQL over a sharded TimeSeries table or over Distributed targets (multi-metric queries failed in the Task 1 compat suite, `e2e/storage/promql_compat_test.go`). The costs of that design:

- the whole metric set must fit on one node (disk and memory), so metrics scale up and by read replicas, not by adding shards;
- a second cluster to define and keep in sync (`deploy/kind/clickhouse.yaml`, configmap `clickhouse-cluster`; real installs need the same);
- special cases in code: `ch.MetricsCluster`, `LowLevelClient.metricsCluster`, the `@on_metrics_cluster` macro, shard-less Keeper paths for the TimeSeries tables (`/clickhouse/tables/metrics/{database}/...`), and a PromQL client that must be pointed at one node.

## The upstream change

[ClickHouse PR #117170](https://github.com/ClickHouse/ClickHouse/pull/117170), "Support PromQL over a Distributed table of TimeSeries shards" (author: valerypetrov). State when this was written (2026-10-06): **open, in review, experimental, no release known**. Not a fork issue: a related fork-only request exists ([ylw510/ClickHouse#7](https://github.com/ylw510/ClickHouse/issues/7), native CLUSTER/SHARD BY) and is not upstream roadmap.

What it provides: PromQL against a `Distributed` table over per-shard `TimeSeries` tables. Raw samples are selected on each shard; PromQL is evaluated on the initiator node, with the same result as one table. Prometheus remote write through the wrapper works.

What it refuses (from the PR description):

- metadata endpoints (`/api/v1/series`, `/api/v1/labels`, `/api/v1/metadata`);
- remote read;
- `timeSeriesData / timeSeriesSamples / timeSeriesTags / timeSeriesMetrics` with a Distributed argument;
- non-trivial row policies and extra table filters on the wrapper.

Related and worth watching: [#121216](https://github.com/ClickHouse/ClickHouse/pull/121216) (bucketed samples, bounded native PromQL execution: read performance), [#122936](https://github.com/ClickHouse/ClickHouse/issues/122936) (duplicate `timeSeriesTags` rows on parallel replicas).

## Trigger

Start this plan when all of these hold:

1. #117170 (or its successor) is in a **stable** ClickHouse release;
2. the feature is documented and either no longer behind an experimental setting or the setting is acceptable to require;
3. a bench (Task below) shows PromQL over the Distributed wrapper is not slower than the single-node path for the evaluator's queries.

Until then: re-run `TestPromQLCompat` against each new ClickHouse version we adopt, with a Distributed case added (Task 1 below can be done earlier, it only needs a version that has the PR).

## What we could simplify

1. **One cluster.** Delete the `coroot_metrics` cluster: metrics become a table in the sharded `coroot` cluster, written and read through a `metrics_distributed` wrapper like logs and traces. Removes `MetricsCluster`, `metricsCluster`, `@on_metrics_cluster` and the one-shard Keeper paths; `info()` in `ch/client.go` stops requiring the second cluster; deploy configs lose it.
2. **Scale by shards.** The metric volume is spread over shards; the "everything on one node" ceiling goes away.
3. **Maybe fewer special paths in the evaluator.** The evaluator still must read through PromQL; `world_*` stays (page latency is its job, not a workaround for sharding). The simplification is limited to where the evaluator's reads go.

Not simplified by this change: the `world_*` rollups, the evaluator's lag, `timestamp()` support, the label parsing in `constructor` (separate spec: `specs/2026-10-01-k8s-only-metric-labels-design.md`).

## Open risks to check before committing to the change

- `promql.Client.MetricNames` and the series-label reads use `timeSeriesTags`/`timeSeriesMetrics`-style access, which the PR refuses on the wrapper. Replacement options: read the per-shard tables with `clusterAllReplicas(coroot, ...)` over the local table, or keep the metric names in a small table fed by the evaluator (`world_series` already has labels per query).
- The collector's insert (`INSERT INTO metrics (metric_name, tags, samples) SELECT ... FROM input(...)`) must work through the wrapper; the PR says remote write works, the `input()` form needs a test. The PR adds `insert_expected_table_engine` / `insert_expected_column_types`; they may need setting in `ch/settings.go` (`TimeSeriesSettings`).
- Sharding key: samples of one series must stay together on one shard. Check what the wrapper shards by (a hash of the series id is the natural key) and that tags and samples of a series land on the same shard.
- Experimental flags (`enable_time_series_table`, `enable_time_series_aggregate_functions`) and a possible new one.
- Cost: with evaluation on the initiator, all raw samples of a query cross the network; compare with the single-node path on the evaluator's heaviest queries (Task 3 below).

## Tasks (when triggered)

### Task 1: Compatibility on a Distributed wrapper
- Extend `e2e/storage/promql_compat_test.go`: create per-shard `compat` tables on the `coroot` cluster and a `compat_distributed` wrapper; run the same queries as the existing cases through it, including a multi-metric query (`rate(a[1m]) / rate(b[1m])`), `sum by`, a missing metric, and `timestamp()` (see the follow-ups plan).
- Expected: identical results to the single table. If anything differs, record it here and stop.

### Task 2: Metadata and label reads without the refused functions
- Change `promql/client.go` (`MetricNames`, label reads) to work on the wrapper using the option chosen under "Open risks". Keep the single-node path until the cutover.
- Test: `TestPromQLClientRoundTrip` passes against the wrapper.

### Task 3: Benchmark
- Run `COROOT_BENCH=1` (`e2e/storage/metrics_bench_test.go`) with metrics in the wrapper, same data set as in the plan's "Task 10 bench" table, and add the numbers next to the single-node ones. Pass criterion: the evaluator's queries are not slower, or the loss is accepted explicitly.

### Task 4: Cut over
- `ch/client.go`: remove `MetricsCluster`, `metricsCluster`, `@on_metrics_cluster`; create `metrics` with `@on_cluster` and `metrics_distributed`; the collector (`collector/metrics.go`) inserts into the wrapper.
- `deploy/kind/clickhouse.yaml`, `Tiltfile`, docs (`docs/docs/configuration/clickhouse.md`, `metrics.md`): one cluster.
- Tests: `TestMetricsWrittenIntoTimeSeries`, `TestPromQLClientRoundTrip`, `TestWorld*`, then `make test-e2e`.
- No migration is planned for old data (new product, no compatibility requirement, same stance as before); metrics older than the TTL window are re-collected.

### Task 5: Update the docs and memory
- `docs/docs/configuration/metrics.md`: remove the "metrics cluster must have one shard" requirement; note the minimum ClickHouse version.
- Mark the matching bullets in `2026-10-05-metrics-followups.md` as done.

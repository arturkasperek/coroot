# Spike: serve the Prometheus metadata API with ClickHouse's native endpoints (future work)

Status: not scheduled, a spike to run when ClickHouse's PromQL leaves preview. Nothing here is implemented.

## Why look at it

Coroot answers `/api/v1/series`, `/api/v1/label/<name>/values` and `/api/v1/metadata` itself (`promql/http.go`: `Series`, `LabelValues`, `MetricMetadata`). `Series` and `LabelValues` turn the `match[]` selectors into SQL over `timeSeriesTags` with `promql/matchers.go` (`matcherSQL`, `matcherSetsSQL`). That is code we own only because ClickHouse did not have these endpoints when it was written.

ClickHouse now has them, on TimeSeries tables (found on the web on 2026-10-08, from the release notes and the docs, not tried against our server):

| Endpoint | In ClickHouse since |
| --- | --- |
| `/api/v1/series` | 26.8 |
| `/api/v1/labels`, `/api/v1/label/<name>/values`, `/api/v1/metadata` | 26.9 |

Our server is 26.9.10.4, so they should exist. If they work and are fast enough, `Series`, `LabelValues`, `MetricMetadata` and `matchers.go` (65 lines + test) could shrink to a thin proxy, and the tag-matching semantics would be ClickHouse's problem, not ours.

## Why it is not done now

- **Preview.** The TimeSeries engine and PromQL are "private preview" in 26.9; the endpoints are marked experimental. `e2e/storage/promql_compat_test.go` exists because behaviour can change between versions.
- **Different transport.** The endpoints are HTTP (under `/prometheus/api/v1/...`, to be configured on the server, with `enable_time_series_table` in the profile of the user the API runs as). Coroot talks to ClickHouse through `clickhouse-go` (native protocol). An HTTP client for ClickHouse, with the project's credentials, TLS and cluster routing, would be new code.
- **Cost unknown.** Our queries read only the tags table (`timeSeriesTags` filtered by `max_time >= now - since`, `promql/client.go` `since = 1h`). `timeSeriesSelector` returns `id, timestamp, value`, so it reads the samples table: do not use it for this. A ClickHouse PR (#121473, "avoid the samples table for /api/v1/labels and /api/v1/label/<name>/values") says the first versions of the label endpoints read samples too. We do not know if it is in 26.9.10.4.
- **Semantics we rely on** may differ: the 1 h activity window, empty label = absent label, `__name__` in every `series` entry, regexes anchored, `limit`, the `start`/`end` parameters.

## What to check (in this order, stop at the first "no")

1. **Reachable.** On a dev ClickHouse (`make dev`): `SELECT version()`, enable the HTTP handler for the Prometheus API on the metrics table, call the five endpoints with `curl`. If any is missing in 26.9.10.4, stop and note the version that has it.
2. **Same answers.** Replay the requests of `e2e/storage/promql_client_test.go` (the `label values`, `series`, `metadata` cases, lines ~165-195) against both implementations on the same data and diff the JSON. Pay attention to: empty labels, `missing=""`, `__name__` in `series`, sort order, `match[]` with several sets, a regex that must be anchored.
3. **Same cost.** On a table with the size of a real project (the metrics bench in `e2e/storage/metrics_bench_test.go` has a generator), compare latency and `system.query_log` read rows/bytes for `labels`, `label/<name>/values`, `series` with and without `match[]`. The native endpoints must not read the samples table for label discovery.
4. **Operations.** Who authenticates (the project's ClickHouse user, `db.IntegrationClickhouse`)? Does it work through the Distributed/cluster setup in `ch.MetricsCluster`, and with the read-only user (see ClickHouse PR #120724 about read-only access to the TimeSeries functions)?

## If all four pass

1. Add a small HTTP client in `promql` next to `Client` (same credentials as `clickhouse.Client`).
2. Replace the bodies of `Series`, `LabelValues`, `MetricMetadata` with a proxy that forwards the request and copies the response; keep our own handlers' input validation tests (`http_test.go`) only if the behaviour stays ours.
3. Delete `promql/matchers.go` and `matchers_test.go`.
4. Keep `QueryRangeHandler` as it is unless the native `query_range` is checked the same way (its error codes and limits, 11,000 points, are ours).
5. Keep `e2e/storage/promql_client_test.go` unchanged: it is the contract, it must pass against the new implementation.

## If one fails

Write down which and on which version in this file, and keep the current code. Re-run the check on the next ClickHouse upgrade (the `promql_compat_test.go` failing is the trigger to look).

## Sources (checked 2026-10-08)

- ClickHouse docs: Prometheus HTTP API and PromQL, `prometheusQueryRange`, `timeSeriesSelector`, 2026 changelog, release 26.9 post.
- ClickHouse PRs #121473 (labels/values without the samples table), #120724 (read-only access), #118124 (private preview tier).

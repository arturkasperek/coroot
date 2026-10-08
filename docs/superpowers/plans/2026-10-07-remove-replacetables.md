# Remove `ReplaceTables` and the `@@table_X@@` placeholders (future work)

Status: not scheduled, low priority. Nothing here is implemented.

## Problem

SQL in the code does not name the Distributed tables directly. It writes `FROM @@table_world_points@@`, and `ch.ReplaceTables` (`ch/client.go:577`) rewrites every `@@table_X@@` to `X_distributed` just before the query is sent:

- `ch.LowLevelClient.Do` (`ch/client.go:99`): every write;
- `clickhouse.Client.Query` / `QueryRow` (`clickhouse/clickhouse.go:87`, `:92`): every read.

The placeholder existed so that one query could go to the local table `X` on a single server and to `X_distributed` on a cluster (the old `useDistributed` flag). Coroot is now cluster-only (Task 1 of `2026-10-05-clickhouse-promql-metrics.md`), so the mapping has one value and is pure indirection:

- about 50 occurrences in production code (`clickhouse/logs.go`, `logs_facets.go`, `queries.go`, `sort.go`, `traces.go`, `traces_facets.go`; `collector/logs.go`, `profiles.go`, `traces.go`; `world/backend.go`, `world/store.go`; `ch/client.go`) and 43 in tests;
- a list `tbls` in `ReplaceTables` that must be kept in step with the tables: a table that is missing from it leaves a literal `@@table_X@@` in the query and fails at run time with a syntax error, not at build time;
- 14 `strings.ReplaceAll` calls per query (negligible cost, but pointless).

## Goal

Queries name `X_distributed` directly. `ReplaceTables`, its list, and the two call sites are gone. No behaviour change.

## Reasons to keep it (decide before starting)

- One place says "read through the Distributed tables": if some tables must later be read differently (for example a local table for metrics, see `2026-10-06-sharded-timeseries-simplification.md`), it is a change in one function, not in 90 places.
- Tests assert query text containing the placeholders (`world/store_test.go`, `clickhouse/*_test.go`); they have to be updated with the code.

If a second read target is expected soon, skip this plan.

## Steps

1. **Check the starting point.** `grep -rn "@@table_" --include='*.go' . | wc -l` (expected about 93: 50 production, 43 test). Run `go test ./... && make test-e2e` once so a later failure is known to be new.
2. **Mechanical rewrite**, production and tests in one pass, for each table in `tbls`:

```bash
for t in otel_logs otel_logs_service_name_severity_text otel_logs_rollup \
         otel_traces otel_traces_trace_id_ts otel_traces_service_name otel_traces_histogram \
         profiling_stacks profiling_samples profiling_profiles \
         world_series world_points world_points_5m world_points_1h; do
  grep -rl "@@table_${t}@@" --include='*.go' . | xargs sed -i '' "s/@@table_${t}@@/${t}_distributed/g"
done
```

   (macOS `sed -i ''`; on Linux `sed -i`.) Longer names first is not needed: the `@@` delimiters make `world_points` and `world_points_5m` distinct.
3. **Delete the mechanism:** `ReplaceTables` and its comment in `ch/client.go`; the call in `LowLevelClient.Do` (`q.Body = ReplaceTables(q.Body)`, the method then just calls `c.pool.Do(ctx, q)`); the two calls in `clickhouse/clickhouse.go` (the methods keep their `ReplaceTables`-free bodies, and the `ch` import may become unused there).
4. **Guard against regressions.** `grep -rn "@@table_" --include='*.go' . ` must print nothing. Add it to a test so it fails in CI: a Go test in `ch/` that walks the repository's `.go` files (excluding `docs/` and itself) and fails on the string, or a line in the `Makefile` test target. Without it a copy-pasted old query would reach the server with a literal placeholder.
5. **Docs.** The comments in `world/store.go`, `world/backend.go` and the plan documents that mention `@@table_X@@` stay as history (do not edit old plans); update any developer docs that explain the placeholder (`grep -rn "@@table" docs CONTRIBUTING.md`).
6. **Verify.** `go build ./... && go vet -tags e2e ./... && go test ./...`, then `make test-e2e` (a missed rewrite shows up as a ClickHouse syntax error or an unknown table, so the e2e suite is the real check, not the unit tests alone).

## Risks

- A placeholder in a string that is not a Go source file (a `.sql` or embedded text): step 1's `grep` is limited to `.go`; run it without `--include` once (`grep -rIn "@@table_" . | grep -v "node_modules\|docs/superpowers"`).
- Queries assembled with `fmt.Sprintf` where the table name is built from a variable: they would not contain the literal `@@table_X@@` and are unaffected, but check them (`grep -rn "_distributed" --include='*.go' .`) so no query ends up with a double suffix (`X_distributed_distributed`).
- A diff over ~12 files with no behaviour change: do it in one sitting, in its own commit, so it is easy to review as "rename only".

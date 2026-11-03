# Rename `constructor.Cache` and its plumbing (future work)

> **Status: done** (2026-11-03). `constructor.Cache` is `constructor.Results`; `QueryCache` / `queryCache` are `QueryResults` / `queryResults`; the field and the variables are `resultsByProject`, `results`, `resultsStatus`, `resultsTo`. `GetStep` is gone from the interface, `world.Client` and `world.Store`: the step of the stored results is the constant `constructor.ResultsStep` (15 s), which replaces `world.Step` (the constructor cannot import `world`, which imports it). The multi-cluster branches took the largest member step, always the same constant. `promql.Client.GetStep` stays: it is that client's own scrape step (used by the MCP metrics tool), not part of the interface. Error and UI strings that say "cache" were left as they are (step 1). Test renamed to `TestQueryResultsReadsRawSLIsOnlyWhenAsked`.

## Problem

After the metrics move to ClickHouse there is no metric cache any more, but the names still say so:

- `constructor.Cache` (`constructor/constructor.go:35`) is an interface of two methods, `QueryRanges(ctx, requests)` (was `QueryRange(ctx, query, from, to, step, fillFunc)`) and `GetStep(from, to)`. Its only implementation is `world.Client`, which reads results the evaluator already stored in the `world_*` tables. `QueryRange` does not evaluate PromQL: the query text is a key.
- `Constructor.cacheClients`, `queryCache`, `QueryCache`, `cacheQuery`, `cacheClient`, `cacheClients`, `cacheStatus` in `constructor`, `api`, `watchers`, `stats`, `collector`, `world` (about 120 occurrences). `constructor.Constructor.QueryCache` (public, used by `api/rca.go`) and `queryCache` (private, used by `LoadWorld`) look like one function defined twice and are not.
- `GetStep` always returns `world.Step` (15 s) in `world.Client`: a method that exists only to satisfy the interface.
- The word "cache" next to `world.Store`, which until 2026-10-30 really had a 5 s in-memory window cache (`windowTTL`), blurred which one is meant. The interface method is now `QueryRanges` (all requests of a run at once) and the store keeps nothing between calls.

## Goal

Names that say what it is: the constructor reads **stored query results**. No behaviour change.

Proposed names (adjust in review):

| now | new |
| --- | --- |
| `constructor.Cache` | `constructor.Results` (or `MetricSource`) |
| `Constructor.cacheClients` | `Constructor.results` |
| `queryCache` / `QueryCache` | `queryResults` / `QueryResults` |
| `cacheQuery` | `resultQuery` |
| `cacheClient`, `cacheClients`, `cacheStatus` (variables) | `results`, `resultsByProject`, `resultsStatus` |
| `GetStep` | drop it: `world.Step` is a constant; `rawStep` in `queryCache` becomes `world.Step` (check the multi-cluster path, where members could have different steps, before removing) |

## Steps

1. **Rename with the compiler's help.** Use gopls rename (or `gofmt -r`) per identifier, one rename per commit-sized step; `go build ./... && go vet -tags e2e ./...` after each. Do not rename strings in log messages or JSON.
2. **Decide `GetStep`.** If every caller gets `world.Step`, delete the method from the interface and `world.Client`, and pass the constant. If the multi-cluster branch of `Api.LoadWorld` / `constructor.LoadWorld` needs a per-project step, keep the method under the new name.
3. **Move the status methods.** `world.Client` also has `GetTo` and `GetStatus` that are not in the interface (they are used by `api` and `watchers` directly). Keep them on `world.Client`; the interface stays minimal.
4. **Docs.** Fix comments that say "cache" for the stored results (`constructor`, `world/store.go`, `world/client.go`, `watchers`), and any mention in `docs/superpowers/plans/2026-10-05-clickhouse-promql-metrics.md` keeps its history (do not edit the old plan).
5. **Verify.** `go test ./...`, front untouched, `make test-e2e` (names only changed: a failure means a wrong rename, not a behaviour difference).

## Risks

- A wide diff (~120 occurrences) that conflicts with any other work in `constructor`, `api`, `watchers`: do it when those areas are quiet, in one sitting.
- External code that embeds `api.Api` or implements `constructor.Cache` (`NewApi` takes `loadWorld` and `licenseMgr` from outside) would break on the rename; check before doing it whether such code exists.
- No performance or behaviour benefit: do this only if the confusing names cost reviewers time.

# Design: ClickHouse-accurate log facet counts

Date: 2026-09-08
Status: Approved

## Problem

Overview Logs sidebar (`LogQuickFilters`) shows counts next to Severity, Cluster, Application, and Host. Those numbers are not one dataset:

- **Severity** sums the histogram series (full selected time range, ClickHouse `GetLogsHistogram`).
- **Cluster / Application / Host** count the currently loaded table page (`query.limit`, default 100).

So `Severity = info` can show `11K` while `Cluster = default` shows `100`. Filters themselves are real (`+` / `-` change the query); only the counts are misleading.

## Goal

Each of the four core facet rows shows an exact `count()` from ClickHouse over the **full selected time range**, in Groundcover style:

- Filters on **other** groups narrow the counts (choose `Severity = error` → Application/Host/Cluster counts drop to errors only).
- Filters on **the same** group are excluded from that group's aggregation (after `Severity = error`, Severity still shows real `info` / `warning` volumes so the user can switch).
- The histogram and log table stay fully filtered (chart shows only errors).

## Non-goals

- ClickHouse aggregations for dynamic attribute groups (`k8s.pod.name`, extra columns, etc.). Those stay client-side from loaded entries.
- Facet sidebar on application logs (`AppLogs.vue` has no sidebar). Do not change that view or `/api/project/{project}/app/{app}/logs`.
- Approximate / sampled / last-hour-only counts.
- A separate `/facets` HTTP endpoint.
- New ClickHouse materialized views or schema changes.

## Architecture

One Search request, same as today. Overview logs JSON gains a `facets` array computed in parallel with histogram + `GetLogs`.

```
UI Search
  → GET /api/project/{id}/overview/logs?from=&to=&query=
      WaitGroup:
        GetLogsHistogram     (all filters, including Severity) → chart
        GetLogs              (all filters, limit)              → entries
        GetLogFacetCounts ×4 (each group excludes its own filters)
      merge per ClickHouse client (multi-cluster)
  → { chart, entries, facets, ... }
```

`LogQuery.filters(attr)` already skips filters whose `Name == *attr`. Facet queries must use that (or equivalent) so exclude-self is the same as QueryBuilder suggest.

## Core facets

| JSON `key` | UI label | Source | Query |
|---|---|---|---|
| `Severity` | Severity | `SeverityNumber` bucketed with the existing `multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1)` | `GROUP BY` bucket, **without** `Severity` filters. Always emit `unknown`, `info`, `warning`, `error` (missing → `count: 0`). |
| `Cluster` | Cluster | Coroot project name on each ClickHouse client, not a table column | `SELECT count()` per client, **without** `Cluster` filters. Value = `project.Name`. |
| `service.name` | Application | `ServiceName` column (`ORDER BY` leading key) | `GROUP BY ServiceName ORDER BY count() DESC LIMIT 1000`, **without** `service.name` filters. Skip empty names. |
| `host.name` | Host | `LogAttributes['host.name']` / `ResourceAttributes['host.name']` (same map join as `GetLogFilters`) | `GROUP BY` value `ORDER BY count() DESC LIMIT 1000`, **without** `host.name` filters. Skip empty strings. |

`LIMIT 1000` matches `GetLogFilters`. Time window is the request `from`/`to` (full selected range, not the 1-hour cap used when discovering attribute **names**).

Agent vs OTEL checkboxes stay on `LogQuery.Source` / `Services` as they do for histogram and entries.

## API contract

Add to `overview.Logs` (JSON field `facets`):

```json
{
  "facets": [
    {
      "key": "Severity",
      "values": [
        { "value": "unknown", "count": 0 },
        { "value": "info", "count": 38 },
        { "value": "warning", "count": 0 },
        { "value": "error", "count": 12 }
      ]
    },
    {
      "key": "Cluster",
      "values": [{ "value": "default", "count": 50 }]
    },
    {
      "key": "service.name",
      "values": [
        { "value": "/k8s/coroot-dev/express-demo", "count": 40 },
        { "value": "/k8s/coroot-dev/nextjs-demo", "count": 10 }
      ]
    },
    {
      "key": "host.name",
      "values": [
        { "value": "node-a", "count": 40 },
        { "value": "node-b", "count": 10 }
      ]
    }
  ]
}
```

Multi-cluster merge: sum `count` for the same `(key, value)` across clients for Severity / Application / Host. Cluster is one row per client (`project.Name`).

Do not include `facets` on the application-logs view.

## Frontend

`Logs.vue` passes `view.logs.facets` into `LogQuickFilters`.

`buildLogQuickFilters` (or a thin wrapper) uses backend values for the four core keys when `facets` is present. Dynamic groups still count `entries`.

`buildStableLogQuickFilters` keeps the existing catalog so `+` / `-` do not drop rows. Backend `count` overwrites catalog counts. An active filter whose value is absent from ClickHouse stays visible with `count: 0`.

Stop deriving Severity sidebar counts from the chart once `facets` includes `Severity`. Chart colors stay as they are.

If `facets` is omitted or `null` (old backend), keep today's client-side counting. If `facets` is present, use it even when a group has no values (do not mix backend Severity with client-side Application).

No visual redesign of the sidebar.

## Error handling

Histogram / `GetLogs` failures stay fatal for the overview logs payload (unchanged).

Facet aggregations are **non-fatal**: log a warning, omit or zero that group, still return `entries` and `chart`. Successful groups are still returned. Same request `context.Context` as the rest of the handler; no extra timeout.

## Tests

Write tests first, then implementation.

### Unit — ClickHouse / overview

Cover query construction and merge without requiring a live cluster:

- Exclude-self: Severity aggregation does not put `Severity` predicates in `WHERE`; `service.name` aggregation does not put `service.name` predicates; other filters remain.
- Severity buckets map to `unknown` / `info` / `warning` / `error`, including zeros for missing buckets.
- Empty `host.name` / `ServiceName` values are dropped.
- Multi-cluster merge sums Application/Host/Severity and keeps separate Cluster rows.

### Unit — frontend

Extend `front/tests/unit/logQuickFilters.test.js`:

- Core groups prefer `facets` counts over `entries.length`.
- With `Severity = error` and Application facets `{express: 10, nextjs: 2}`, those counts render (not the unfiltered 40/10).
- Severity still shows `info` count from facets (not 0).
- Missing `facets` falls back to counting entries.

### E2E — ingest then overview API

New `//go:build e2e` test in `coroot/e2e/`, run by `make test-e2e` against a running `make dev` cluster.

**Ingest.** Do not use `make seed`. Insert a fixed 50-row fixture over ClickHouse HTTP (`COROOT_DEV_CLICKHOUSE_HTTP`, default `http://127.0.0.1:18123`), same JSONEachRow shape as `scripts/chseed`. Resolve the `otel_logs` database the same way chseed does (default DB on make-dev). Tag every row with `LogAttributes['e2e.facets'] = <unique token>`. Service names are agent-style (`/k8s/coroot-dev/...`). Set `ResourceAttributes['host.name']`. Timestamps are within `now-15m`. `t.Cleanup` deletes `WHERE LogAttributes['e2e.facets'] = '<token>'`.

Fixture:

| severity | `ServiceName` | `host.name` | count |
|---|---|---|---|
| info | `/k8s/coroot-dev/express-demo` | `node-a` | 30 |
| error | `/k8s/coroot-dev/express-demo` | `node-a` | 10 |
| info | `/k8s/coroot-dev/nextjs-demo` | `node-b` | 8 |
| error | `/k8s/coroot-dev/nextjs-demo` | `node-b` | 2 |

**Query.** `GET /api/project/{id}/overview/logs?from=now-15m&query=...` with `agent: true`, `otel: true`, and filter `e2e.facets = <token>` so live agent logs and `chseed` rows cannot change counts.

**Assert.**

1. Isolation filter only: Application 40 / 10, Host 40 / 10, Severity `info=38` `error=12` (`warning`/`unknown` = 0), Cluster total = 50.
2. Add `Severity = error`: Application 10 / 2, Host 10 / 2; Severity still `info=38` `error=12`.
3. Replace with `service.name = /k8s/coroot-dev/express-demo`: Severity `info=30` `error=10`; Application still lists both services with unfiltered-by-self counts (40 and 10).

Helper: `fetchOverviewLogs(t, projectID, query)` next to `fetchAppLogsQuery`. Decode `data.logs.facets` from the overview envelope.

## Files (expected)

- `clickhouse/logs.go` — `GetLogFacetCounts` (and Cluster `count()`).
- `api/views/overview/logs.go` — parallel fetch, merge, JSON `facets`.
- `front/src/utils/logQuickFilters.js`, `LogQuickFilters.vue`, `Logs.vue`.
- Tests: `clickhouse/` and/or `api/views/overview/` unit tests, `front/tests/unit/logQuickFilters.test.js`, `e2e/log_facets_test.go` plus a small ClickHouse HTTP helper in `e2e/`.

## Success criteria

On overview Logs, with more than 100 matching rows, Application/Host/Cluster counts match ClickHouse for the selected range and filters (not `limit`). Choosing `Severity = error` reduces Application counts. Severity sidebar still shows other levels' true volumes. Histogram and table remain filtered. `make test` and `make test-e2e` pass with the new coverage.

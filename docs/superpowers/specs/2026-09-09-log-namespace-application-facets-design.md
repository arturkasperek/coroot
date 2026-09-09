# Design: Namespace and Application log facets

Date: 2026-09-09
Status: Approved

## Problem

Overview Logs **Application** is a facet over raw ClickHouse `ServiceName`. Agent container logs are `/k8s/<namespace>/<app>`, so the sidebar shows `coroot-dev/express-demo`. There is no way to filter by Kubernetes namespace. OpenTelemetry logs often use a short `service.name` (`express-demo`) and may carry `k8s.namespace.name`; systemd/Docker logs have no k8s namespace at all.

## Goal

- New **Namespace** facet: filter by Kubernetes namespace.
- **Application** shows the short app name for k8s workloads (`express-demo`), not `ns/app`.
- Logs with no k8s namespace go to Namespace value `n/a`, displayed as **Not applicable**.
- Counts and filters are computed in ClickHouse (same pattern as **Source**), not by parsing the loaded page in the browser.

## Non-goals

- App-level logs UI (`AppLogs.vue` / `/app/{id}/logs`).
- Schema changes or materialized views.
- Inferring namespace from arbitrary keys (`namespace`, `k8s_ns`, …) beyond the rules below.
- Browser e2e of the sidebar; API + SQL are the e2e surface.
- Backward-compatible rewriting of old `service.name = /k8s/ns/app` chips into `Application` / `Namespace` (raw `service.name` filters keep working).

## Derivation

Evaluate in this order per log row:

**Namespace**

1. If `ServiceName` starts with `/k8s` (covers `/k8s/...` and `/k8s-cronjob/...`): take the path segment after the runtime prefix (`/k8s/coroot-dev/express-demo` → `coroot-dev`; `/k8s-cronjob/coroot-dev/backup` → `coroot-dev`). This wins even if `k8s.namespace.name` disagrees.
2. Else if `ResourceAttributes['k8s.namespace.name']` is non-empty, use it (OpenTelemetry semantic conventions).
3. Else if `LogAttributes['k8s.namespace.name']` is non-empty, use it.
4. Else `n/a`.

**Application**

1. If `ServiceName` starts with `/k8s` and has a name segment: last path segment (`express-demo`, `backup`).
2. Else the full `ServiceName` (`express-demo`, `checkout`, `/system.slice/ssh.service`).

## Filter and facet contract

JSON filter names (QueryBuilder chips):

| `name` | UI label | Stored value | Chip example |
|---|---|---|---|
| `Namespace` | Namespace | real NS or `n/a` | `Namespace = coroot-dev`, `Namespace = Not applicable` |
| `Application` | Application | extracted app name | `Application = express-demo` |

Operators: `=` (OR across values) and `!=` (AND NOT), same as Source / Severity.

Exclude-self: `GetLogFacetCounts("Namespace")` omits `Namespace` predicates; `GetLogFacetCounts("Application")` omits `Application` predicates. Other filters including raw `service.name` still apply.

Namespace facet **always** includes `n/a` (count may be 0). Other namespaces appear only when count > 0, ordered by count desc then name. Application: skip empty names, `ORDER BY count DESC LIMIT 1000`, no padding.

Sidebar `+` / `−` write `Namespace` / `Application` filters, not raw `service.name`. QueryBuilder still lists raw `service.name` as an attribute; old chips `{ name: "service.name", value: "/k8s/coroot-dev/express-demo" }` still hit the `ServiceName =` path.

## API / backend

Extend `facetCountSQL` and `LogQuery.filters` with `Namespace` and `Application` cases (derived expressions, not table columns). `GetLogFilters("")` suggest names include `Namespace` and `Application`. Static suggest values for `Namespace` / `Application` are not a closed enum (except `n/a` is always a legal Namespace value).

`addFacet` + `facetGroupsFromMerged` order:

`Source`, `Severity`, `Cluster`, `Namespace`, `Application`, `host.name`

Stop emitting core facet key `service.name` once `Application` exists (sidebar uses `Application`).

Kubernetes Events page: hide `Namespace` and `Application` (already hides `service.name` and `Source`). Column `object.namespace` is unchanged.

Facet query failures stay non-fatal (same as other facet aggregations).

## Frontend

`CORE_FACETS`: insert Namespace; replace `service.name` with `{ key: 'Application', label: 'Application' }`.

Local search: Namespace and Application (and Host). `n/a` label **Not applicable** so search `not` / `n/a` matches.

`formatLogFilter`: Namespace `n/a` → Not applicable; Application values are already short.

No Application/Namespace checkboxes. Same `+`/`−` controls as other groups.

## Tests (write first)

### Unit — ClickHouse / overview

- Namespace SQL/filter: `/k8s/`, `/k8s-cronjob/`, Resource vs Log `k8s.namespace.name`, fallback `n/a`, path wins over attribute.
- Application SQL/filter: last k8s segment vs full `ServiceName`.
- `=` / `!=`; exclude-self (`filters("Namespace")` has no Namespace predicate).
- Merged facet order includes Namespace then Application; `n/a` always present in Namespace values.

### Unit — frontend

- Display `n/a` as Not applicable; k8s service names as short Application labels.
- Group order: Source, Severity, Cluster, Namespace, Application, Host.
- Local search on Namespace/Application.

### E2E — insert into ClickHouse, then overview API

Same ingest pattern as `TestOverviewLogSourceFilters`: ClickHouse HTTP JSONEachRow, unique `LogAttributes['e2e.facets']` token, timestamps ~2 minutes behind wall clock, `t.Cleanup` delete by token. Isolation filter `e2e.facets = token`. Do not depend on `make seed` or live pods. Requires `make dev`. Extend the existing insert helper so a row can set extra `ResourceAttributes` (for `k8s.namespace.name`) without changing `ServiceName`.

Fixture (61 rows):

| rows | Namespace | Application | count |
|---|---|---|---|
| `/k8s/coroot-dev/express-demo` | coroot-dev | express-demo | 15 |
| `/k8s/coroot-dev/nextjs-demo` | coroot-dev | nextjs-demo | 7 |
| `/k8s-cronjob/coroot-dev/backup` | coroot-dev | backup | 2 |
| `/k8s/kube-system/coredns` | kube-system | coredns | 11 |
| `/k8s/kube-system/express-demo` | kube-system | express-demo | 4 |
| OTEL `ServiceName=express-demo`, `ResourceAttributes['k8s.namespace.name']=coroot-dev` | coroot-dev | express-demo | 8 |
| OTEL `express-demo`, no NS attr | n/a | express-demo | 5 |
| OTEL `checkout`, no NS attr | n/a | checkout | 6 |
| `/system.slice/ssh.service` | n/a | `/system.slice/ssh.service` | 3 |

Totals: `coroot-dev=32`, `kube-system=15`, `n/a=14`.

Assert facets **and** matching entries:

1. Isolation only: those Namespace totals.
2. `Application=express-demo` → 15+4+8+5=32 (both namespaces + OTEL with/without NS).
3. `Namespace=coroot-dev` + `Application=express-demo` → 15+8=23.
4. `Namespace=kube-system` + `Application=express-demo` → 4.
5. `Namespace=n/a` + `Application=express-demo` → 5.
6. `Namespace=coroot-dev` + `Source=agent` → 32−8=24 (drop OTEL-with-attr).
7. `Namespace != coroot-dev` → kube-system + n/a = 29.
8. Self-exclusion: with `Namespace=coroot-dev`, Namespace facet still lists all three buckets.
9. Empty: `Namespace=kube-system` + `Application=nextjs-demo` → cluster total 0.

Run: `make test-e2e TestOverviewLogNamespaceFilters` (name may vary; `-run` via existing Makefile `RUN=` / positional args).

## Files (expected)

- `clickhouse/logs.go`, `clickhouse/logs_facets.go` (+ unit tests)
- `api/views/overview/logs.go`, `logs_facets.go` (+ unit tests)
- `front/src/utils/logQuickFilters.js`, `QueryBuilder.vue`, `Logs.vue`, `views/Kubernetes.vue`
- `front/tests/unit/logQuickFilters.test.js`
- `e2e/log_namespace_test.go` (or similar), reuse `e2e/clickhouse.go` insert helper

## Success criteria

On overview Logs, Namespace lists k8s namespaces plus **Not applicable**. Application lists short names. `+` on a namespace writes a `Namespace` query chip and narrows Application counts. Same app name in two namespaces is separable via Namespace. OTEL with `k8s.namespace.name` joins the k8s bucket; OTEL/systemd without it sit in Not applicable. Unit tests and the ingest e2e pass.

# Remove the usage-statistics reporter (future work)

Status: not scheduled. Nothing here is implemented. Reason: in a new, independent product Coroot Inc's anonymous-usage reporter is not needed, and it sends data to a third party (`https://coroot.com/ce/usage-statistics`) by default.

## What it is today

`stats/stats.go` (596 lines, package `stats`, type `stats.Collector`; not to be confused with the telemetry `collector.Collector`):

- **`send()`** (in the background, every `collectInterval`) runs `collect()`, then profiles itself (CPU with `pprof.StartCPUProfile`, heap with `godeltaprof`), base64-encodes both into the report and `POST`s the JSON to `collectUrl` (`stats.go:30`).
- **`collect()`** builds the report: instance id and version, database type, edition, installation type (`INSTALLATION_TYPE`), counts of projects, nodes, CPU cores, GPUs, applications by kind, instances, deployments and audit summaries, kernel versions, clouds, services and instrumented services, which integrations are configured (ClickHouse, alerting integrations, FluxCD, ArgoCD, cloud costs), inspection overrides, load and audit times, query statistics of the constructor, and sent notifications (`db.GetSentIncidentNotificationsStat`). To get the numbers it builds a `World` for every project, so it also costs CPU (`ctr.LoadWorld`, `auditor.Audit`).
- **UI tracking:** the middleware (`MiddleWare`, `RegisterRequest`) counts API calls and page views per device, theme, screen size and user role; the front end calls `POST /stats` from `front/src/api.js` (`api.stats(...)`, `navigator.sendBeacon`) on every route change (`front/src/main.js:107`).
- **MCP:** `RegisterMCPCall(tool)` counts MCP tool calls (`api/mcp.go:136`).
- It can already be switched off (`--disable-usage-statistics`, `DISABLE_USAGE_STATISTICS`, YAML `disable_usage_statistics`), and `NewCollector(disabled, ...)` then does nothing. Documented in `docs/docs/misc/anonymous-usage-statistics.md`.

## Why remove it instead of leaving the flag

- It is on by default and phones home to a domain this product does not own.
- It builds a `World` of every project on a timer only to count things.
- It carries dependencies and code that serve nothing else: `godeltaprof` (`github.com/grafana/pyroscope-go/godeltaprof` in `go.mod`), `runtime/pprof` use, `utils.GetInstanceUuid` and the `instance.uuid` file in `DataDir` (check other users first), `db.GetSentIncidentNotificationsStat`.
- Fewer places that read `World` internals (the report touches nodes, applications, instances, deployments, audit reports), so fewer things to keep compiling when the model changes.
- A user-facing disclosure page and a flag to explain.

## Steps

1. **Find every user before deleting.** `grep -rn "stats\.\|statsCollector\|DisableUsageStatistics\|usage-statistics\|GetInstanceUuid\|INSTALLATION_TYPE\|deploymentUuid\|instanceUuid" --include='*.go' --include='*.md' --include='*.yaml' --include='*.js' --include='*.vue' . | grep -v "node_modules\|docs/superpowers"` and list what remains outside `stats/` (at the time of writing: `main.go`, `api/api.go`, `api/mcp.go`, `config/config.go`, `config/flags.go`, `front/src/api.js`, `front/src/main.js`, docs).
2. **Server:**
   - `main.go`: delete `stats.NewCollector(...)`, `router.Use(statsCollector.MiddleWare)` and both `/stats` handlers (`POST` and `GET`, lines ~221-234); `NewApi` loses its `stats` argument.
   - `api/api.go`: remove the field `stats *stats.Collector`, the parameter and the import; `api/mcp.go`: `AddTool` registers the handler directly (no `RegisterMCPCall`).
   - `config/config.go`, `config/flags.go`: remove `DisableUsageStatistics` and the flag; `docs/docs/configuration/configuration.md`: remove the flag row (line ~39) and the YAML key (line ~118).
   - Delete the `stats/` directory.
   - `db/incident.go`: delete `GetSentIncidentNotificationsStat` if nothing else uses it.
   - `utils.GetInstanceUuid` and the `deploymentUuid` / `instanceUuid` values in `main.go`: keep only if something else needs them (`NewApi` takes both); decide after the grep in step 1.
3. **Front end:** delete `stats(type, data)` from `front/src/api.js` (and `deviceId` if only it used it), the `api.stats('route-open', ...)` call and the `to.meta.stats` handling in `front/src/main.js`, and `meta.stats` entries in the router. `cd front && npm run lint && npm run test:unit && npm run build`.
4. **Docs:** delete `docs/docs/misc/anonymous-usage-statistics.md` and fix links to it (`grep -rn "anonymous-usage-statistics" docs`); `docs/` build must pass (broken links fail it).
5. **Dependencies:** `go mod tidy` (expect `godeltaprof` to go; check `go.sum`).
6. **Verify:** `go build ./... && go vet -tags e2e ./... && go test ./...`; `make test-e2e`; start Coroot, open a few pages, check in the browser's network tab that no `POST /stats` is sent and in the server log that nothing reports to `coroot.com`.

## Risks

- `NewApi`'s signature changes (it already has `nil` placeholders for `licenseMgr` and `loadWorld`); any external code that calls it (the Enterprise edition lives elsewhere) breaks. Decide whether that matters before starting.
- The `/stats` GET page (an authenticated JSON view of the report, `statsCollector.Stats`) is also removed; if someone uses it for debugging, a replacement is not planned.
- If a product-analytics need appears later, add it on purpose (opt-in, own endpoint) rather than reviving this code.

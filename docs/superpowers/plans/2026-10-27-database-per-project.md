# ClickHouse comes only from the deployment, every project gets its own database

Status: plan for later, nothing of it started. Repository: `coroot`.

Decided 2026-10-28: the ClickHouse of every project comes from the
deployment configuration (`GLOBAL_CLICKHOUSE_*`, set by the Helm chart), never
from the UI or the API. The decision of 2026-10-27 that a hand-set
integration wins over the global one is reversed: there is no hand-set
integration any more.

## Where we are

Coroot has three ways to give a project its ClickHouse database:

| Mode | How it is set | Database of a project |
|---|---|---|
| Global | `GLOBAL_CLICKHOUSE_ADDRESS` (+ `_USER`, `_PASSWORD`, `_INITIAL_DATABASE`, TLS) | `coroot_<projectId>`, created by the collector (`collector/migration.go`, `CreateDB ... ON CLUSTER`) |
| Bootstrap | `BOOTSTRAP_CLICKHOUSE_ADDRESS` (+ `_DATABASE`) | written into the default project's integration once (`config/bootstrap.go:26`); `BOOTSTRAP_CLICKHOUSE_DATABASE` or `default` |
| Per project | the Clickhouse tab of the project settings (`/p/<id>/settings/clickhouse`, `front/src/views/IntegrationClickhouse.vue`), saved by `PUT /api/project/{project}/integrations/clickhouse` | whatever is typed |

`Project.ClickHouseConfig` (`db/project.go:93`) prefers the integration stored
in the project over the global one.

What this costs:
- **Replicas have to notice changes at runtime.** A per-project integration
  is saved on the one replica that got the request (`api/api.go:990`,
  `collector.UpdateClickhouseClient`); the others notice it in their 10 s
  project reload. For that the collector keeps the last seen configuration of
  every project and drops its client and migration when it changes
  (`collector/collector.go`, `setProjects`, `clickhouseConfigs`).
- **Two projects can point to one database.** Their telemetry mixes (the
  tables have no project column), and each ClickHouse migration runs once per
  project on that database (`clickhouse_schema` is keyed by
  `(project_id, target)`).
- **The form offers a protocol that does not work** (HTTP, see
  `2026-10-20-clickhouse-http-protocol.md`).
- **Dev and production run different modes.** `make dev` uses the bootstrap
  mode (`deploy/kind/coroot.yaml`: `BOOTSTRAP_CLICKHOUSE_ADDRESS=clickhouse:9000`),
  so the dev project writes to `default` (schema target `default@coroot`) and
  a second project created in the dev UI gets no ClickHouse at all. A bug of
  the global mode (the database creation, `coroot_<id>` names) is never seen
  in dev.

## Goal

- One mode: the global ClickHouse. Every project, the default one and any
  created in the UI or by tests, gets `coroot_<projectId>` automatically.
- The ClickHouse settings change only with a deployment: the pods restart
  and every replica reads them at start. Nothing to notice at runtime.
- Coroot does not start without `GLOBAL_CLICKHOUSE_ADDRESS` (the fork needs
  ClickHouse anyway: metrics, traces, logs, profiles all live there).
- `make dev` runs exactly the production mode.
- Dev tooling finds a project's database from its id, not by guessing.

Out of scope, accepted: one project can't be put on another ClickHouse
server than the rest (e.g. for isolation). If it is ever needed, it comes
back as deployment configuration (a per-project override in the config
file), not as a UI form.

## 1. Only the global ClickHouse

### Configuration (`config/`)

- `GLOBAL_CLICKHOUSE_ADDRESS` (or `global_clickhouse.address` in the config
  file) is required: `config.Validate` fails with "GLOBAL_CLICKHOUSE_ADDRESS
  is required". Its protocol is native only (the HTTP plan's option A is done
  by this: no protocol can be chosen any more).
- Remove the bootstrap mode: `BootstrapClickhouse` (`config/config.go`),
  `GetBootstrapClickhouse`, the `bootstrap-clickhouse-*` flags and
  `BOOTSTRAP_CLICKHOUSE_*` variables (`config/flags.go`), and its use in
  `config/bootstrap.go:26-33`.
- A start with any `BOOTSTRAP_CLICKHOUSE_*` variable or flag set fails with
  "BOOTSTRAP_CLICKHOUSE_* is removed, use GLOBAL_CLICKHOUSE_ADDRESS (every
  project gets its own database)", instead of ignoring it silently. Keep the
  flags only for that message (hidden), or check the environment.
- In this repository only `deploy/kind/coroot.yaml` sets it (section 3); no
  page of `docs/docs` mentions it (checked 2026-10-27). The Helm chart and the
  operator (other repositories, see `2026-10-20-multi-replica-followups.md`
  section 4) switch to `GLOBAL_CLICKHOUSE_*`.

### `Project.ClickHouseConfig` (`db/project.go:93`)

- Returns the global configuration with `Database = "coroot_" + id`.
- The one exception: projects with `remoteCoroot` in the config file
  (`config/bootstrap.go:70`, protocol `coroot`). They come from the
  deployment configuration too and are written into the project at every
  start, so the stored integration is used when its protocol is `coroot`, and
  ignored otherwise.
- The `globalClickHouse` parameter stops being optional: every caller
  (`api`, `collector`, `world`, `stats`, `watchers`, `clickhouse.GetClients`)
  gets a non-nil one. Remove the `globalClickHouse != nil` / `== nil`
  branches (e.g. `api/api.go:351`, `api/api.go:989`).

### Stored per-project integrations

- At startup (`config/bootstrap.go`), for every project whose
  `settings.integrations.clickhouse` is set and whose protocol is not
  `coroot`: log a warning ("project X had its own ClickHouse <addr>/<db>, it
  now uses the global one, database coroot_X; its old data is not read") and
  remove the setting.
- No production install has one (the fork is not released); in dev the
  default project has `clickhouse:9000` / `default` from the bootstrap mode
  and is cleared by this (section 3).

### API and UI

- `api/forms/forms.go`: remove `IntegrationFormClickhouse` and its case in
  `NewIntegrationForm`. `api.Integration` answers PUT, POST and DELETE for
  the type `clickhouse` with 405 "ClickHouse is configured by the deployment
  (GLOBAL_CLICKHOUSE_*)"; GET keeps the cluster info.
- `front/src/views/IntegrationClickhouse.vue`: a read-only view: the
  address (masked as today for non-admins), the project's database
  `coroot_<id>`, the cluster info (shards, replicas, disk), and a line saying
  the settings come from the deployment. No form, no Save, no Test, no
  protocol radio.
- `front/src/views/Project.vue:325` (the redirect to the Clickhouse tab when
  ClickHouse is not configured) is removed: it is always configured.

### Collector (`collector/`)

- Remove the runtime change detection: `clickhouseConfigs` and the
  "settings changed" part of `setProjects` (`collector/collector.go`), and
  `UpdateClickhouseClient`. `setProjects` stays only if it still does more
  than storing the map; otherwise it folds back into `updateProjects`.
- Keep `loadedProject` and the batches resolving the project by id: once a
  project is deleted its batches stop writing.
- Keep `MigrateClickhouseDatabase` at project creation (`api/api.go:352`):
  the creating replica makes `coroot_<id>` at once; the others find the
  project in their 10 s reload and their migration loop finds the version
  already recorded.
- Update `collector/projects_test.go` to what is left.

## 2. What this settles in other plans

- `2026-10-20-clickhouse-http-protocol.md`: option A happens here (no
  protocol to choose). Mark it superseded once this is done; its check of
  production settings for `http` is not needed (nothing released).
- Two projects can no longer share a database: `coroot_<id>` is per id. The
  former section "reject two projects sharing a database" is dropped.

## 3. `make dev` on the global ClickHouse

- `deploy/kind/coroot.yaml`: replace `BOOTSTRAP_CLICKHOUSE_ADDRESS` with
  `GLOBAL_CLICKHOUSE_ADDRESS=clickhouse:9000`
  (`GLOBAL_CLICKHOUSE_INITIAL_DATABASE` stays `default`: the database the
  collector connects to before creating `coroot_<id>`).
- The dev project's stored `clickhouse:9000` / `default` integration is
  removed by the startup step of section 1; no dev script is needed.
- The old `default` database of dev keeps its data and is no longer read.
  Write in the dev docs how to get rid of it (drop its tables, or reset the
  dev ClickHouse volume).

## 4. Dev tooling resolves the database from the project id

The tools guess today:
- `e2e/clickhouse.go` `pickLogsDatabase`: `default` if it has `otel_logs`,
  else the first `coroot_*` alphabetically;
- `scripts/chseed/parse.go`: the first `coroot_*`, else `default`.

With several projects in dev the first `coroot_*` is any project.

- Both read the project id from the Coroot API (`GET /api/user` gives the
  projects; `scripts/chseed` already parses it, `ParseUserProjects`) and use
  `coroot_<id>`. With several projects, take the one named `default` (the
  bootstrap project) unless `COROOT_DEV_PROJECT` names another.
- `scripts/dev/dev.sh`: the example queries print the real database
  (`coroot_<id>.otel_traces`), not `default.otel_traces`.
- `e2e/replicas_test.go` `TestProjectCreatedOnOneReplicaIngestsOnTheOther`
  creates a database and sets the project's integration by hand, which is no
  longer possible. Rewrite it: create the project on one replica, send spans
  to the other, read them from `coroot_<projectId>`. It then tests the
  production path: the database created by the replica that did not create
  the project.

## Tests

- **Unit, `config`:**
  - a start without `GLOBAL_CLICKHOUSE_ADDRESS` fails;
  - a start with `BOOTSTRAP_CLICKHOUSE_ADDRESS` set fails with the message
    pointing to `GLOBAL_CLICKHOUSE_ADDRESS`.
- **Unit, `db`:** `ClickHouseConfig` of a project with a stored native
  integration gives the global one with `coroot_<id>`; with a stored `coroot`
  protocol integration gives that one.
- **Unit, `collector`:** what is left of `projects_test.go` (a deleted
  project's batches stop writing).
- **Storage (`e2e/storage`):**
  - two projects on the global ClickHouse get `coroot_<id1>` and
    `coroot_<id2>`, each migrated (`clickhouse_schema` has one row per
    project);
  - the startup step removes a stored native integration and keeps a `coroot`
    one.
- **Dev e2e:**
  - `PUT /api/project/<id>/integrations/clickhouse` returns 405 and leaves the
    project's settings unchanged;
  - after `make dev`, the default project's spans are in
    `coroot_<projectId>.otel_traces`, none arrive in `default.otel_traces`;
  - a project created through the API on one replica gets its database and
    ingests on the other within one migration pass (the rewritten replicas
    test);
  - the full `make test-e2e` passes with the tools resolving the database
    from the project id.

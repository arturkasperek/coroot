# Postgres required, SQLite removed (future work)

Status: not scheduled. Nothing here is implemented. This is a product decision as much as a code change: it was discussed as a consequence of "new product, no compatibility to keep, Kubernetes only".

## Why

Coroot keeps its configuration and state (projects, users, API keys, alerting rules, alerts, incidents, settings) in a SQL database that is either embedded SQLite or Postgres (`--pg-connection-string`, SQLite when it is not set; `main.go:61-67`). Two code paths for one store cost us:

- **Two dialects.** `db/db.go` branches on `typ` for the unique-violation error (`IsUniqueViolationError`), for `AddColumnIfNotExists` (`pragma_table_info` vs `ADD COLUMN IF NOT EXISTS`), for `Migrator.Exec` (`INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT` rewritten to `SERIAL PRIMARY KEY` for Postgres, used in `db/user.go:30`), and for `PRAGMA foreign_keys` and `SetMaxOpenConns(1)`. Every new migration must work in both.
- **HA only works on Postgres.** `GetPrimaryLock` (`db/lock.go`) returns `true` unconditionally for SQLite, and uses `pg_try_advisory_lock(1)` on Postgres. The world evaluator (`world/run.go`), the watchers and the Coroot instances rely on it so that only one instance writes. With SQLite a second replica cannot exist (the docs say so: `docs/docs/guides/argocd.md`, `installation/k8s-operator.md`: "required if replicas > 1").
- **A cgo dependency.** `github.com/mattn/go-sqlite3` needs cgo; dropping it simplifies the build and the image.
- **Local state on disk.** SQLite needs a persistent volume and `data_dir`; the pod is a StatefulSet-like thing with state, not a Deployment.
- **Untested path.** Our dev cluster (`deploy/kind/coroot.yaml`) already runs Postgres, so the SQLite path is exercised only by `e2e/storage/world_test.go` (`db.NewSqlite(t.TempDir())`, two tests) and `CONTRIBUTING.md`.

Why Postgres and not ClickHouse for this state: it is small, mutable, transactional data with unique constraints and a primary lock (an OLTP workload); see the reasoning in the discussion of 2026-10-06 (mutations in ClickHouse are heavy and asynchronous, no unique keys, no cross-table transactions, and each project's ClickHouse address is itself a setting).

## Goal

Coroot starts only with a Postgres connection string. `db.NewSqlite`, `TypeSqlite`, the SQLite branches and `go-sqlite3` are gone; `Type` is dropped (one dialect) or left as a constant. No migration of existing SQLite data (no compatibility requirement for a new product).

## Steps

1. **Make the connection string required.** `config/flags.go:23` (`--pg-connection-string`, "sqlite is used if not set") and `config/config.go` (`Postgres.ConnectionString`): validate at startup and exit with a clear message when it is empty. Rename nothing else (flag name and `PG_CONNECTION_STRING` stay).
2. **`main.go`.** Replace the `if/else` (lines 61-67) with `db.NewPostgres(cfg.Postgres.ConnectionString)` only. Keep `cfg.DataDir` for `instance.uuid` and `cloud-pricing` (`main.go:90`, `main.go:124`): the directory is still used, but it no longer holds the database, so it may become an `emptyDir` (the instance uuid is regenerated, the pricing cache is rebuilt).
3. **Delete the SQLite code in `db/`.** `NewSqlite`, `TypeSqlite`, the `sqlite3` import, the `IsUniqueViolationError` branch, the `AddColumnIfNotExists` SQLite branch (leave only `ADD COLUMN IF NOT EXISTS`), the `AUTOINCREMENT` rewrite (write `SERIAL PRIMARY KEY` in `db/user.go` directly and delete the `strings.ReplaceAll`). `GetPrimaryLock` loses its `typ != TypePostgres` early return. `Type()` / `DatabaseType` in `stats/stats.go:320` always reports `postgres` (or drop the field; update `docs/docs/misc/anonymous-usage-statistics.md`).
4. **Tests.** `e2e/storage/world_test.go` (two tests using `db.NewSqlite(t.TempDir())`) need a Postgres: reuse the dev cluster's Postgres through a forwarded port (add a `5432` forward next to the ClickHouse ones in the `Tiltfile`, export `COROOT_DEV_POSTGRES_DSN` in `scripts/dev/dev-storage-urls.sh`, create a throw-away schema or database per test like `ch/chtest` does for ClickHouse). Add a small `db/dbtest` helper for it. `db/db_test.go` is only about `addPostgresConnectTimeout` and stays.
5. **Dependencies.** `go mod tidy` removes `mattn/go-sqlite3`; check the Dockerfile and CI for `CGO_ENABLED` / gcc that existed only for it (`grep -rn "CGO\|gcc" Dockerfile* Makefile .github`).
6. **Deploy and docs.**
   - `deploy/kind/coroot.yaml` already sets `PG_CONNECTION_STRING`; nothing to change there.
   - Docs: `docs/docs/configuration/database.md` (drop the "SQLite (default)" section; Postgres is the only one), `configuration.md` (flag description, the `postgres:` YAML block comment), `high-availability.md` (no more "by default SQLite"), `installation/k8s-operator.md` line ~297 and `docs/guides/argocd.md` (Option A "Single instance with SQLite" becomes "single instance with Postgres"), `CONTRIBUTING.md` (log lines).
   - The Helm charts and the operator live in other repositories: the default `postgres` section there must become mandatory too (out of scope here; list as a follow-up in those repos).
7. **Verify.** `go build ./... && go vet -tags e2e ./... && go test ./...`; start Coroot against the dev Postgres, create a project and an API key, restart, check the data survived; start with an empty connection string and check the error message; `make test-e2e`.

## Risks and open questions

- **Product:** a laptop or single-VM quick start now needs a Postgres next to Coroot. In Kubernetes that is one more small Deployment (already true for ClickHouse and its Keeper). Decide whether the Helm chart should bring its own Postgres by default.
- **Existing SQLite installations** lose their data on upgrade unless an export/import tool is written. Not planned: this is a new product. If an installation exists that matters, add a one-off `coroot migrate-sqlite --from db.sqlite --to $PG_CONNECTION_STRING` command before step 3 and keep it for one release.
- **Postgres is a new hard dependency for startup order** (like ClickHouse). Check how the readiness probe and the Tilt resource order (`resource_deps=['clickhouse', 'postgres']`, already there) behave when Postgres is down: `database.Migrate()` in `main.go:70` currently exits on error; consider retry with backoff instead of a crash loop.
- **Integer-boolean columns** (`boolToInt` in `db/db.go`) and other SQLite-isms could be turned into native types, but that is a separate cleanup, not required.

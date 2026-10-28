# Versioned schema migrations for Postgres and ClickHouse

Status: design approved 2026-10-20, not implemented. Repository: `coroot`.

## Problem

Both databases are migrated today by idempotent DDL run at every start:

- Postgres: each type in `db/` has `Migrate(m *Migrator)` with
  `CREATE TABLE IF NOT EXISTS` and `AddColumnIfNotExists`; `db.Migrate()`
  runs them all under `WithLock(MigrationLockId)` (`main.go`).
- ClickHouse: `ch.LowLevelClient.Migrate` runs the lists `tables`,
  `distributedTables` and `timeSeriesTables` (`ch/client.go`), with
  `@on_cluster`, `@merge_tree`, `@ttl_*` substituted, per project, from the
  collector loop (`collector/migration.go`) under
  `WithLock(ClickhouseMigrationLockId)`.

No database records which schema it has. `IF NOT EXISTS` compares names, not
definitions: a changed sorting key, column type, MV query or a dropped column
is skipped without an error, and the code runs on the old schema. Until now
the fix was dropping the tables; in production that loses customer data.

## Goal

From the first production deployment on, every schema change is a numbered
migration that runs exactly once per database, in order, and existing data
survives. Today's schema becomes version 1.

## Decisions

- Postgres: goose (`github.com/pressly/goose/v3`, Provider API).
- ClickHouse: our own small mechanism, versions stored in Postgres.
- Migrations keep running inside Coroot, from the same places as today: at
  startup for Postgres, in the collector loop for ClickHouse (a project
  created or reconfigured at runtime gets its tables within one loop). No
  Kubernetes Job.
- Database per project stays as it is (`coroot_<projectId>` on the global
  ClickHouse, or the project's own integration).

## 1. Postgres with goose

- Migrations are SQL files in `db/migrations/`, embedded with `embed.FS`,
  named `00001_baseline.sql`, `00002_<name>.sql`, ...
- `00001_baseline.sql` is today's schema exactly: every
  `CREATE TABLE IF NOT EXISTS` from `db/*.go` with the columns that
  `AddColumnIfNotExists` adds folded into the table definitions, plus the
  same statements `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` for databases
  created before a column existed, and the indexes.
  - On an empty database it creates everything.
  - On a database created by an earlier Coroot it changes nothing, and goose
    records version 1.
- `db.Migrate()` builds `goose.NewProvider(goose.DialectPostgres, db, fsys)`
  and calls `Up` inside the existing `WithLock(MigrationLockId)`: one lock
  mechanism for both databases. goose's own locker is not used.
- Removed: the `Migrate(m *Migrator)` methods of the types in `db/`, the
  `Table` interface, `Migrator`, `AddColumnIfNotExists`, and the
  `extraTables` parameter of `db.Migrate`.
- goose keeps its versions in `goose_db_version`.

## 2. ClickHouse, own mechanism

### Where the version lives

A Postgres table, created by the goose baseline:

```sql
CREATE TABLE IF NOT EXISTS clickhouse_schema (
    project_id TEXT NOT NULL,
    target     TEXT NOT NULL,
    version    INTEGER NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_id, target)
)
```

- `target` names the ClickHouse database the project writes to: the database
  and the cluster the client found (`<database>@<cluster>`). Not the address:
  replicas reaching one ClickHouse by different names share the version.
- A recorded version is trusted only if the database has `otel_traces` (a
  table every version keeps); otherwise the database was dropped or recreated
  (a wiped ClickHouse, a project moved to another server with the same
  database name) and is migrated from version 0.
- A project switched to another database has a new `target`, so the new
  database starts at version 0. Rows of old targets stay; they
  are deleted with the project.
- Postgres was chosen over a table inside each ClickHouse database: it is
  shared by all replicas, transactional, already locked by `WithLock`, and
  avoids a replicated version table on the cluster.

### Migrations

- `ch/migrations.go` holds an ordered list:

  ```go
  type Migration struct {
      Version    int
      Name       string
      Statements []string                                    // run in order, after substitution
      Run        func(ctx context.Context, c *LowLevelClient) error // instead of Statements, for data copies
  }
  ```

- Statements use the substitutions `Migrate` applies today (`@on_cluster`,
  `@cluster`, `@merge_tree`, the replicated engines, `@ttl_*`); the
  substitution moves into one function used by every migration.
- Version 1 is today's `tables`, `distributedTables` and `timeSeriesTables`
  (the time series ones keep `TimeSeriesSettings`), unchanged, with
  `IF NOT EXISTS`: an existing database goes through it without changes.
- Rule for authors: a migration must be safe to run again
  (`IF NOT EXISTS`, `IF EXISTS`, `INSERT ... SELECT` guarded by a check),
  because ClickHouse has no transactions. A migration that fails half way
  leaves the version unchanged and runs again on the next pass. There is no
  "dirty" flag.
- Versions are consecutive integers; a unit test checks the list is
  ordered with no gaps or duplicates.

### Running

`ch.LowLevelClient.Migrate(ctx, config, versions)` is replaced by a function
in the collector (`collector/migration.go`, `migrateProject`):

1. Connect, create the database if global (as today).
2. Under `WithLock(ClickhouseMigrationLockId)`:
   1. read `version` for `(project_id, target)` (0 when no row);
   2. for every migration with a higher version, in order: run it, then
      upsert the row with its version (`INSERT ... ON CONFLICT DO UPDATE`);
   3. stop at the first error, keep the reached version.
3. `migrationDone[project]` is set when the version equals the latest the
   code knows. Ingestion of the project waits for that, as today.

The loop, its 10 s interval, the backoff and the three call sites
(the loop, project creation, integration change) stay.

## 3. Rolling updates with several replicas

During a rollout an old and a new replica run together on the schema the
new one migrated.

- A release only extends the schema: new columns, tables, MVs next to the
  old ones. Dropping or renaming happens in a later release, once no replica
  runs code that uses the old object (expand, then contract).
- A replica whose code knows a lower version than the database has logs it
  once per project and keeps working; it does not block.
- `docs/` gets a page for migration authors: these rules, and recipes for
  the ClickHouse changes with no `ALTER`:
  - a new sorting key: new table, MV or code writing to both, background
    `INSERT ... SELECT` of the old data, switch reads, drop the old table in
    the next release;
  - a changed MV: create the new MV (new target table) next to the old one,
    backfill the period before its creation, switch reads, drop the old MV
    later.

## 4. Out of scope

- TTL reconciliation: a changed `*_TTL` config does not alter existing
  tables today, and still will not. Listed as a follow-up.
- Kubernetes Job for migrations: rejected, Coroot keeps migrating itself.
- A shared ClickHouse database with a `ProjectId` column: rejected for
  simplicity.

## 5. Tests

Storage tests (`e2e/storage`, real Postgres via `dbtest` and ClickHouse via
`chtest`):

- Postgres:
  - empty database → latest version, every table present;
  - database created by the old code (the baseline statements run without
    goose) → version 1 recorded, schema unchanged
    (compare `information_schema.columns` before and after);
  - two `db.DB` migrating at once → both succeed, one `goose_db_version`
    row per version.
- ClickHouse:
  - empty database → latest version, every table present;
  - database created by the old `Migrate` → version 1 recorded, no DDL
    error, tables unchanged (`system.tables.create_table_query`);
  - two collectors migrating one project at once → each migration recorded
    once, no error;
  - a test migration failing at its second statement → version stays, the
    next pass finishes it;
  - a project whose `target` changes → the new database migrates from 0;
  - a test migration 2 (add a column) on a version 1 database with rows →
    the rows are still there and the column exists.
- Unit: the migration list is consecutive; the substitution of placeholders.

Then the full `make test-e2e`, and a deployment to dev with 2 replicas:
the existing dev databases reach version 1 with no change and no data loss.

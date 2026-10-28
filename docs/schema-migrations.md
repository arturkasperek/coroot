# Schema migrations

Coroot migrates its databases itself, at startup (Postgres) and per project
from the collector (each project's ClickHouse database), one replica at a
time under a Postgres advisory lock.

## Postgres

A change is a new file in `db/migrations/`, the next number:
`00003_<what>.sql`, with `-- +goose Up` and `-- +goose Down` (`-- forward
only`). A released file never changes. goose runs each file in a
transaction and records it in `goose_db_version`.

## ClickHouse

A change is a new `ch.Migration` at the end of `ch.Migrations`, the next
version. The version of each project's database is in Postgres
(`clickhouse_schema`, keyed by project and `<database>@<cluster>`). A
database without `otel_traces` is migrated from version 1 again, whatever
its recorded version (a wiped ClickHouse): keep that table in every version,
or change `schemaSentinelTable` with it. A released migration
never changes, and `tables`, `distributedTables` and `timeSeriesTables`
(version 1) are never edited.

ClickHouse has no transactions: a migration that fails half way runs again
from its start. Write every statement so that running it twice is harmless:
`IF NOT EXISTS`, `IF EXISTS`, an `INSERT ... SELECT` that checks what is
already copied.

## Rolling updates

During a rollout, replicas of the previous release run on the schema the new
release migrated. So a release only adds: columns, tables, views next to the
old ones. Dropping or renaming comes in a later release, when no running code
uses the old object.

## Changes ClickHouse can't ALTER

- **A new sorting key, partition key or engine:** create the new table
  (`<name>_v2`), make the code write to both tables (or an MV from the old
  one), copy the old data with `INSERT ... SELECT` in a `Run` step in time
  ranges, switch the reads, drop the old table in the next release.
- **A changed materialized view:** create the new MV with a new target table
  next to the old one; backfill the target for the period before the MV was
  created with `INSERT ... SELECT` from the source table; switch the reads;
  drop the old MV and its table in the next release.
- **A new column:** add it to the local table and to its `_distributed`
  table (a Distributed table keeps the columns it was created with), in the
  same migration.
- **A column type:** `ALTER TABLE ... MODIFY COLUMN` is a mutation that
  rewrites the column in every part; on large tables prefer a new column
  filled by `DEFAULT`/`MATERIALIZED` and switch the reads.

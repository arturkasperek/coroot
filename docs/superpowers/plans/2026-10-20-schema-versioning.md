# Versioned schema migrations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every schema change of Postgres and of each project's ClickHouse database runs once, in order, recorded as a version, with today's schema as version 1.

**Architecture:** Postgres migrations become goose SQL files embedded in `db/`, applied by `db.Migrate()` at startup under the existing advisory lock. ClickHouse migrations become an ordered `[]ch.Migration`, applied per project by the collector; the version of each project's ClickHouse database is a row in the Postgres table `clickhouse_schema` keyed by `(project_id, target)`.

**Tech Stack:** Go 1.25, `github.com/pressly/goose/v3` (Provider API), `lib/pq`, `ch-go` (`ch.LowLevelClient`), Postgres, ClickHouse cluster.

**Spec:** `docs/superpowers/specs/2026-10-20-schema-versioning-design.md`

## Global Constraints

- Naming (AGENTS.md): descriptive names, no abbreviations (`config`, not `cfg`, in new code); constructors `New<StructName>`; no function that only forwards its arguments.
- Migrations keep running inside Coroot: Postgres at startup (`main.go` → `db.Migrate()`), ClickHouse from the collector loop and its two call sites. No Kubernetes Job.
- One lock mechanism: `db.WithLock` with `MigrationLockId` (Postgres) and `ClickhouseMigrationLockId` (ClickHouse). goose's own locker is not used.
- Version 1 of both databases is today's schema with `IF NOT EXISTS`: a database created by the earlier code passes version 1 without any change.
- Every ClickHouse migration must be safe to run again; a failed one leaves the version unchanged. No "dirty" flag.
- A replica whose code knows a lower ClickHouse version than the database has logs it and keeps working.
- Do not commit: the user commits. Leave the changes in the working tree.
- Storage tests carry the `e2e` build tag and run with `make test-e2e` (needs the dev cluster port-forwards: ClickHouse `127.0.0.1:19000`, Postgres `127.0.0.1:15432`).

## Review Focus

- A Postgres database created by the earlier code (no `goose_db_version`) must reach version 1 with its columns unchanged — Task 1 `TestPostgresBaselineOnAnOldDatabase`.
- A ClickHouse database created by the earlier code must reach version 1 with no DDL error and identical `create_table_query` — Task 3 `TestClickhouseBaselineOnAnOldDatabase`.
- Two replicas starting at once must not apply a migration twice or fail — Task 1 `TestPostgresMigrateConcurrently`, Task 3 `TestClickhouseMigrateConcurrently`.
- A project moved to another ClickHouse database must be migrated from 0 there, not considered done — Task 4 `TestProjectMovedToAnotherDatabaseMigratesFromZero`.
- A replica running older code (fewer migrations than the database has) must not fail or block ingestion — Task 3 `TestClickhouseOlderCodeKeepsWorking`.

---

## File Structure

- Create `db/migrations/00001_baseline.sql` — today's Postgres schema.
- Create `db/migrations/00002_clickhouse_schema.sql` — the version table of the ClickHouse databases (a separate migration on top of the baseline, so goose's step from 1 to 2 is exercised from day one; the spec's "created by the goose baseline" is satisfied in effect: every database has it after `Migrate`).
- Create `db/migrations.go` — `embed.FS`, `Migrate()` with goose, the startup project reconciliation.
- Modify `db/db.go` — remove `Migrate`, `Table`, `Migrator`, `NewMigrator`, `AddColumnIfNotExists`.
- Modify `db/*.go` (`project.go`, `check_configs.go`, `incident.go`, `alert_notification.go`, `deployments.go`, `application_settings.go`, `dashboards.go`, `settings.go`, `user.go`, `alert.go`, `mcp_session.go`) — remove their `Migrate(m *Migrator)` methods.
- Create `db/clickhouse_schema.go` — read and write of `clickhouse_schema`.
- Modify `db/project.go` — `DeleteProject` deletes the project's `clickhouse_schema` rows.
- Create `ch/migrations.go` — `Migration`, `Migrations`, `ExecMigration`, `Migrate`, `Target`.
- Modify `ch/client.go` — remove the old `Migrate`; keep `tables`, `distributedTables`, `timeSeriesTables` as the content of version 1; store the target in `LowLevelClient`.
- Create `ch/migrations_test.go` — unit tests of the list and of the substitution.
- Modify `ch/chtest/chtest.go` — migrate through the versioned path with a Postgres from `dbtest`.
- Modify `collector/migration.go` — `migrateProject` calls `client.Migrate`.
- Create `e2e/storage/postgres_migrations_test.go`, `e2e/storage/clickhouse_migrations_test.go`.
- Modify `e2e/storage/migrate_test.go` — the replay test uses the new API.
- Create `docs/docs/development/schema-migrations.md` (or the closest existing developer docs folder) — rules for migration authors.
- Modify `docs/superpowers/plans/2026-10-20-multi-replica-followups.md` — TTL reconciliation follow-up.

---

### Task 1: Postgres migrations with goose

**Files:**
- Create: `db/migrations/00001_baseline.sql`, `db/migrations/00002_clickhouse_schema.sql`, `db/migrations.go`
- Modify: `db/db.go:99-183` (remove `Migrator`, `Migrate`, `Table`), the `Migrate(m *Migrator)` methods in `db/*.go`
- Modify: `go.mod`, `go.sum`
- Test: `e2e/storage/postgres_migrations_test.go`

**Interfaces:**
- Produces: `func (db *DB) Migrate() error` (same name and callers as today: `main.go:63`, `db/dbtest/dbtest.go:62`, `e2e/storage/primary_lock_test.go:82`); `const PostgresSchemaVersion = 2` is not added — tests read the version from goose.
- Produces: the Postgres table `clickhouse_schema(project_id TEXT, target TEXT, version INTEGER, updated_at TIMESTAMPTZ, PRIMARY KEY (project_id, target))`.

- [ ] **Step 1: Add goose**

Run: `go get github.com/pressly/goose/v3@latest && go mod tidy`
Expected: `go.mod` lists `github.com/pressly/goose/v3`. If the newest release needs a newer Go than 1.25.5, pin the newest one that builds (`go build ./...`).

- [ ] **Step 2: Write the failing tests**

`e2e/storage/postgres_migrations_test.go`:

```go
//go:build e2e

package storage

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/db/dbtest"
	"github.com/stretchr/testify/require"
)

// emptyPostgres creates an empty database (no schema) and returns its DSN.
func emptyPostgres(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("COROOT_DEV_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("COROOT_DEV_POSTGRES_DSN is not set")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("t_empty_%d", time.Now().UnixNano()%1e9)
	_, err = admin.Exec("CREATE DATABASE " + name)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)") })
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

func gooseVersion(t *testing.T, database *db.DB) int64 {
	t.Helper()
	var version int64
	require.NoError(t, database.QueryRow("SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&version))
	return version
}

func columnsOf(t *testing.T, database *db.DB) []string {
	t.Helper()
	rows, err := database.Query(`
		SELECT table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable || ' ' || coalesce(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name NOT IN ('goose_db_version', 'clickhouse_schema')
		ORDER BY 1`)
	require.NoError(t, err)
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}
	return columns
}

func TestPostgresMigrateEmptyDatabase(t *testing.T) {
	database := dbtest.Open(t, emptyPostgres(t))
	require.NoError(t, database.Migrate())
	require.EqualValues(t, 2, gooseVersion(t, database))
	for _, table := range []string{"project", "check_configs", "incident", "incident_notification", "alert_notification",
		"application_deployment", "application_settings", "dashboards", "settings", "users", "alerting_rule", "alert",
		"mcp_session", "clickhouse_schema"} {
		var exists bool
		require.NoError(t, database.QueryRow("SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists))
		require.True(t, exists, table)
	}
	require.NoError(t, database.Migrate(), "a second start changes nothing")
	require.EqualValues(t, 2, gooseVersion(t, database))
}

// A database created by the earlier Coroot (the baseline statements run with
// no goose) reaches version 1 and its columns stay as they were.
func TestPostgresBaselineOnAnOldDatabase(t *testing.T) {
	database := dbtest.Open(t, emptyPostgres(t))
	baseline, err := db.MigrationFiles.ReadFile("migrations/00001_baseline.sql")
	require.NoError(t, err)
	_, err = database.Exec(string(baseline)) // the goose annotations are SQL comments
	require.NoError(t, err)
	before := columnsOf(t, database)

	require.NoError(t, database.Migrate())
	require.EqualValues(t, 2, gooseVersion(t, database))
	require.Equal(t, before, columnsOf(t, database))
}

// Two replicas starting at once: both succeed, each version applied once.
func TestPostgresMigrateConcurrently(t *testing.T) {
	dsn := emptyPostgres(t)
	first, second := dbtest.Open(t, dsn), dbtest.Open(t, dsn)
	var group sync.WaitGroup
	results := make([]error, 2)
	for index, database := range []*db.DB{first, second} {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index] = database.Migrate()
		}()
	}
	group.Wait()
	require.NoError(t, results[0])
	require.NoError(t, results[1])
	var applied int
	require.NoError(t, first.QueryRow("SELECT count(*) FROM goose_db_version WHERE version_id > 0").Scan(&applied))
	require.Equal(t, 2, applied)
}
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `make test-e2e RUN='TestPostgres'`
Expected: compile error `undefined: db.MigrationFiles`.

- [ ] **Step 4: Write `db/migrations/00001_baseline.sql`**

The statements are today's `Migrate(m *Migrator)` methods in the order of `db.Migrate` (`project` first: the others reference it), with the columns `AddColumnIfNotExists` adds kept as `ALTER ... ADD COLUMN IF NOT EXISTS`, so that a database from before a column existed gets it:

```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS project (
	id TEXT NOT NULL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	prometheus TEXT
);
ALTER TABLE project ADD COLUMN IF NOT EXISTS settings text;

CREATE TABLE IF NOT EXISTS check_configs (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	configs TEXT,
	PRIMARY KEY (project_id, application_id)
);

CREATE TABLE IF NOT EXISTS incident (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	key TEXT NOT NULL,
	opened_at INT NOT NULL,
	resolved_at INT NOT NULL DEFAULT 0,
	severity INT NOT NULL,
	PRIMARY KEY (project_id, application_id, opened_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS incident_key ON incident (project_id, key);
CREATE INDEX IF NOT EXISTS incident_project_id_resolved_opened ON incident (project_id, (resolved_at = 0), opened_at);
ALTER TABLE incident ADD COLUMN IF NOT EXISTS details text;
ALTER TABLE incident ADD COLUMN IF NOT EXISTS rca text;
ALTER TABLE incident ADD COLUMN IF NOT EXISTS rca_status text;
ALTER TABLE incident ADD COLUMN IF NOT EXISTS rca_summary text;

CREATE TABLE IF NOT EXISTS incident_notification (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	incident_key TEXT NOT NULL,
	status INT NOT NULL,
	destination TEXT NOT NULL,
	timestamp INT NOT NULL,
	sent_at INT NOT NULL DEFAULT 0,
	external_key TEXT NOT NULL DEFAULT '',
	details TEXT
);

CREATE TABLE IF NOT EXISTS alert_notification (
	project_id TEXT NOT NULL REFERENCES project(id),
	alert_id TEXT NOT NULL,
	rule_id TEXT NOT NULL,
	application_id TEXT NOT NULL,
	status INT NOT NULL,
	destination TEXT NOT NULL,
	timestamp INT NOT NULL,
	sent_at INT NOT NULL DEFAULT 0,
	external_key TEXT NOT NULL DEFAULT '',
	details TEXT
);

CREATE TABLE IF NOT EXISTS application_deployment (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	name TEXT NOT NULL,
	started_at INT NOT NULL,
	finished_at INT NOT NULL DEFAULT 0,
	details TEXT,
	metrics_snapshot TEXT,
	notifications TEXT,
	PRIMARY KEY (project_id, application_id, started_at)
);

CREATE TABLE IF NOT EXISTS application_settings (
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	settings TEXT NOT NULL,
	PRIMARY KEY (project_id, application_id)
);

CREATE TABLE IF NOT EXISTS dashboards (
	project_id TEXT NOT NULL REFERENCES project(id),
	id TEXT NOT NULL,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	config TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (project_id, id)
);

CREATE TABLE IF NOT EXISTS settings (
	name TEXT NOT NULL PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
	id SERIAL PRIMARY KEY,
	email TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL,
	password TEXT NOT NULL,
	roles TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS alerting_rule (
	id TEXT NOT NULL,
	project_id TEXT NOT NULL REFERENCES project(id),
	config TEXT NOT NULL,
	enabled INT NOT NULL DEFAULT 1,
	builtin INT NOT NULL DEFAULT 0,
	created_at INT NOT NULL,
	updated_at INT NOT NULL,
	PRIMARY KEY (id, project_id)
);
CREATE INDEX IF NOT EXISTS alerting_rule_project ON alerting_rule (project_id);

CREATE TABLE IF NOT EXISTS alert (
	id TEXT PRIMARY KEY,
	fingerprint TEXT NOT NULL,
	rule_id TEXT NOT NULL,
	project_id TEXT NOT NULL REFERENCES project(id),
	application_id TEXT NOT NULL,
	application_category TEXT NOT NULL DEFAULT '',
	severity TEXT NOT NULL,
	summary TEXT NOT NULL,
	details TEXT,
	opened_at INT NOT NULL,
	resolved_at INT NOT NULL DEFAULT 0,
	updated_at INT NOT NULL,
	suppressed INT NOT NULL DEFAULT 0,
	resolved_by TEXT NOT NULL DEFAULT '',
	report TEXT NOT NULL DEFAULT '',
	pattern_words TEXT NOT NULL DEFAULT '',
	manually_resolved_at INT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS alert_project_resolved ON alert (project_id, resolved_at);
CREATE INDEX IF NOT EXISTS alert_fingerprint_resolved ON alert (fingerprint, resolved_at);
CREATE INDEX IF NOT EXISTS alert_fingerprint_suppressed ON alert (fingerprint, suppressed);
CREATE INDEX IF NOT EXISTS alert_project_rule_resolved ON alert (project_id, rule_id, resolved_at);

CREATE TABLE IF NOT EXISTS mcp_session (
	id TEXT NOT NULL PRIMARY KEY,
	project_id TEXT NOT NULL,
	updated_at BIGINT NOT NULL
);

-- +goose Down
-- forward only
```

Before writing it, diff it against the `Migrate(m *Migrator)` methods in `db/*.go` once more (`grep -n "CREATE\|AddColumnIfNotExists" db/*.go`): any statement there and missing here is a bug.

- [ ] **Step 5: Write `db/migrations/00002_clickhouse_schema.sql`**

```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS clickhouse_schema (
	project_id TEXT NOT NULL,
	target TEXT NOT NULL,
	version INTEGER NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (project_id, target)
);

-- +goose Down
-- forward only
```

- [ ] **Step 6: Write `db/migrations.go`**

```go
package db

import (
	"context"
	"embed"
	"io/fs"

	"github.com/pressly/goose/v3"
	"k8s.io/klog"
)

// MigrationFiles are the Postgres schema migrations, applied in order by
// Migrate. 00001_baseline is the schema of the Coroot that had no versions:
// IF NOT EXISTS everywhere, so a database it created passes it unchanged. A
// new change of the schema is a new file; a file already released never
// changes.
//
//go:embed migrations/*.sql
var MigrationFiles embed.FS

// Migrate brings the schema to the latest version, then updates the data that
// depends on the code (the builtin alerting rules of every project, the old
// form of application categories). Replicas starting at once migrate one
// after another.
func (db *DB) Migrate() error {
	ctx := context.Background()
	return db.WithLock(ctx, MigrationLockId, func() error {
		migrations, err := fs.Sub(MigrationFiles, "migrations")
		if err != nil {
			return err
		}
		provider, err := goose.NewProvider(goose.DialectPostgres, db.db, migrations)
		if err != nil {
			return err
		}
		results, err := provider.Up(ctx)
		if err != nil {
			return err
		}
		for _, result := range results {
			klog.Infof("postgres schema migration %s applied in %s", result.Source.Path, result.Duration)
		}
		return db.updateProjectsForThisVersion()
	})
}

// updateProjectsForThisVersion adds the builtin alerting rules this version
// of Coroot has and a project lacks, and moves application categories to their
// current form. It runs at every start: it depends on the code, not on the
// schema.
func (db *DB) updateProjectsForThisVersion() error {
	projects, err := db.GetProjects()
	if err != nil {
		return err
	}
	for _, project := range projects {
		if err = db.migrateApplicationCategories(project); err != nil {
			return err
		}
		_ = db.InitBuiltinAlertingRules(project.Id)
	}
	return nil
}
```

Check against the old code: `Project.Migrate` ran `migrateApplicationCategories` for each project and returned its error; `AlertingRule.Migrate` ran `InitBuiltinAlertingRules` and ignored its error. Keep both behaviors.

- [ ] **Step 7: Remove the old mechanism**

- In `db/db.go` delete `Migrator()`, the old `Migrate(extraTables ...Table)`, `type Table`, `type Migrator`, `NewMigrator`, `(*Migrator).Migrate`, `(*Migrator).Exec`, `AddColumnIfNotExists`.
- Delete every `func (x *T) Migrate(m *Migrator) error` in `db/*.go` (list in File Structure).
- Run `go build ./... && go vet ./db/...` and fix what references them.

- [ ] **Step 8: Run the tests**

Run: `make test-e2e RUN='TestPostgres|TestPrimaryLock|TestMcpSession'`
Expected: PASS. `dbtest.New` goes through the new `Migrate`, so every storage test that uses Postgres covers it too.

- [ ] **Step 9: Leave for the user to commit** (no `git commit`).

---

### Task 2: The version store of ClickHouse databases

**Files:**
- Create: `db/clickhouse_schema.go`
- Modify: `db/project.go` (`DeleteProject`)
- Test: `e2e/storage/postgres_migrations_test.go` (append)

**Interfaces:**
- Consumes: the table `clickhouse_schema` (Task 1).
- Produces:
  - `func (db *DB) GetClickhouseSchemaVersion(projectId ProjectId, target string) (int, error)` — 0 when there is no row.
  - `func (db *DB) SetClickhouseSchemaVersion(projectId ProjectId, target string, version int) error` — upsert.

- [ ] **Step 1: Write the failing test**

```go
func TestClickhouseSchemaVersions(t *testing.T) {
	database := dbtest.New(t)
	version, err := database.GetClickhouseSchemaVersion("p1", "ch:9000/coroot_p1@coroot")
	require.NoError(t, err)
	require.Equal(t, 0, version, "no row: nothing migrated")

	require.NoError(t, database.SetClickhouseSchemaVersion("p1", "ch:9000/coroot_p1@coroot", 1))
	require.NoError(t, database.SetClickhouseSchemaVersion("p1", "ch:9000/coroot_p1@coroot", 3))
	version, err = database.GetClickhouseSchemaVersion("p1", "ch:9000/coroot_p1@coroot")
	require.NoError(t, err)
	require.Equal(t, 3, version)

	version, err = database.GetClickhouseSchemaVersion("p1", "other:9000/coroot_p1@coroot")
	require.NoError(t, err)
	require.Equal(t, 0, version, "another database of the same project starts from 0")
}

func TestDeleteProjectDeletesClickhouseSchemaVersions(t *testing.T) {
	database := dbtest.New(t)
	project := &db.Project{Name: "versions"}
	id, err := database.SaveProject(project)
	require.NoError(t, err)
	require.NoError(t, database.SetClickhouseSchemaVersion(id, "ch:9000/coroot_x@coroot", 1))
	require.NoError(t, database.DeleteProject(id))
	version, err := database.GetClickhouseSchemaVersion(id, "ch:9000/coroot_x@coroot")
	require.NoError(t, err)
	require.Equal(t, 0, version)
}
```

Check the signature of `SaveProject` in `db/project.go` before running and adapt the two lines that create the project.

- [ ] **Step 2: Run to see it fail**

Run: `make test-e2e RUN='TestClickhouseSchemaVersions|TestDeleteProjectDeletesClickhouseSchemaVersions'`
Expected: compile error, `GetClickhouseSchemaVersion` undefined.

- [ ] **Step 3: Implement `db/clickhouse_schema.go`**

```go
package db

import (
	"database/sql"
	"errors"
)

// GetClickhouseSchemaVersion is the schema version of a project's ClickHouse
// database, the target (see ch.LowLevelClient.Target); 0 when none of its
// migrations has run.
func (db *DB) GetClickhouseSchemaVersion(projectId ProjectId, target string) (int, error) {
	var version int
	err := db.db.QueryRow(
		"SELECT version FROM clickhouse_schema WHERE project_id = $1 AND target = $2", projectId, target,
	).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return version, err
}

func (db *DB) SetClickhouseSchemaVersion(projectId ProjectId, target string, version int) error {
	_, err := db.db.Exec(`
		INSERT INTO clickhouse_schema (project_id, target, version, updated_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (project_id, target) DO UPDATE SET version = excluded.version, updated_at = excluded.updated_at`,
		projectId, target, version)
	return err
}
```

In `DeleteProject`, before `DELETE FROM project`:

```go
	if _, err = tx.Exec("DELETE FROM clickhouse_schema WHERE project_id = $1", id); err != nil {
		return err
	}
```

- [ ] **Step 4: Run the tests** — same command, expected PASS.

---

### Task 3: ClickHouse migrations

**Files:**
- Create: `ch/migrations.go`, `ch/migrations_test.go`, `e2e/storage/clickhouse_migrations_test.go`
- Modify: `ch/client.go` (remove `Migrate`, add the target), `ch/chtest/chtest.go`, `e2e/storage/migrate_test.go`

**Interfaces:**
- Consumes: `db.(*DB).GetClickhouseSchemaVersion`, `SetClickhouseSchemaVersion` (Task 2), `db.(*DB).WithLock`, `db.ClickhouseMigrationLockId`.
- Produces:
  - `type Migration struct { Version int; Name string; Statements []string; Settings []chgo.Setting; Run func(ctx context.Context, client *LowLevelClient) error }`
  - `var Migrations []Migration`
  - `func (c *LowLevelClient) Target() string` — `<addr>/<database>@<cluster>`
  - `func (c *LowLevelClient) ExecMigration(ctx context.Context, config config.CollectorConfig, migration Migration) error`
  - `func (c *LowLevelClient) Migrate(ctx context.Context, config config.CollectorConfig, database *db.DB, projectId db.ProjectId, migrations []Migration) error`
  - `chtest.Env` gains `Postgres *db.DB` and `ProjectId db.ProjectId`.

- [ ] **Step 1: Write the failing unit tests** (`ch/migrations_test.go`)

```go
package ch

import (
	"testing"

	"github.com/coroot/coroot/config"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

// Versions start at 1 and go up by one: a gap or a duplicate would skip a
// migration or run two under one version.
func TestMigrationsAreConsecutive(t *testing.T) {
	for index, migration := range Migrations {
		assert.Equal(t, index+1, migration.Version, migration.Name)
		assert.NotEmpty(t, migration.Name)
		assert.True(t, len(migration.Statements) > 0 || migration.Run != nil, migration.Name)
	}
}

func TestMigrationStatementSubstitutesPlaceholders(t *testing.T) {
	day := 24 * 3600 * timeseries.Second
	statement := migrationStatement(
		"CREATE TABLE t (a Int8) ENGINE @merge_tree TTL toDateTime(a) + toIntervalSecond(@ttl_traces)",
		config.CollectorConfig{TracesTTL: 2 * day},
	)
	assert.Equal(t,
		"CREATE TABLE t (a Int8) ENGINE ReplicatedMergeTree('/clickhouse/tables/{shard}/{database}/{table}', '{replica}') TTL toDateTime(a) + toIntervalSecond(172800)",
		statement)
}
```

Check that `TracesTTL` renders as seconds the way the old `Migrate` did (`fmt.Sprintf("%d", cfg.TracesTTL)`); if `timeseries.Duration` is in seconds, `2*day` is `172800`.

- [ ] **Step 2: Run to see it fail**

Run: `go test ./ch/ -run 'TestMigrations|TestMigrationStatement'`
Expected: compile error, `Migrations` undefined.

- [ ] **Step 3: Implement `ch/migrations.go`**

```go
package ch

import (
	"context"
	"fmt"
	"strings"

	chgo "github.com/ClickHouse/ch-go"
	"github.com/coroot/coroot/config"
	"github.com/coroot/coroot/db"
	"k8s.io/klog"
)

// Migration is one change of the schema of a project's ClickHouse database.
// It runs once per database, in the order of Version, recorded in Postgres
// (db.SetClickhouseSchemaVersion). ClickHouse has no transactions: a migration
// that fails half way runs again from its start, so each must be safe to run
// again (IF NOT EXISTS, IF EXISTS, a guarded INSERT ... SELECT). A released
// migration never changes; a new change is a new migration at the end.
type Migration struct {
	Version    int
	Name       string
	Statements []string // with the placeholders of migrationStatement
	Settings   []chgo.Setting
	// Run is for what statements can't express (copying data in batches); it
	// runs after Statements.
	Run func(ctx context.Context, client *LowLevelClient) error
}

// Migrations is the schema of a project's ClickHouse database, version by
// version. Version 1 is the schema of the Coroot that had no versions: a
// database it created passes it unchanged.
var Migrations = []Migration{
	{
		Version:    1,
		Name:       "baseline",
		Statements: append(append(append([]string{}, tables...), distributedTables...), timeSeriesTables...),
		Settings:   TimeSeriesSettings, // needed by the TimeSeries tables, ignored by the others
	},
}

// migrationStatement fills the placeholders that depend on the configuration;
// the cluster ones are filled by ExecWithSettings.
func migrationStatement(statement string, config config.CollectorConfig) string {
	statement = strings.ReplaceAll(statement, "@ttl_traces", fmt.Sprintf("%d", config.TracesTTL))
	statement = strings.ReplaceAll(statement, "@ttl_logs", fmt.Sprintf("%d", config.LogsTTL))
	statement = strings.ReplaceAll(statement, "@ttl_profiles", fmt.Sprintf("%d", config.ProfilesTTL))
	statement = strings.ReplaceAll(statement, "@ttl_metrics", fmt.Sprintf("%d", config.MetricsTTL))
	statement = strings.ReplaceAll(statement, "@merge_tree", "ReplicatedMergeTree('/clickhouse/tables/{shard}/{database}/{table}', '{replica}')")
	statement = replacingByVersion.ReplaceAllString(statement, "ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/{database}/{table}', '{replica}', $1)")
	statement = strings.ReplaceAll(statement, "@replacing_merge_tree", "ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/{database}/{table}', '{replica}')")
	statement = strings.ReplaceAll(statement, "@summing_merge_tree", "ReplicatedSummingMergeTree('/clickhouse/tables/{shard}/{database}/{table}', '{replica}')")
	statement = strings.ReplaceAll(statement, "@aggregating_merge_tree", "ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/{database}/{table}', '{replica}')")
	return statement
}

// ExecMigration runs one migration, without recording it.
func (c *LowLevelClient) ExecMigration(ctx context.Context, config config.CollectorConfig, migration Migration) error {
	for _, statement := range migration.Statements {
		if err := c.ExecWithSettings(ctx, migrationStatement(statement, config), migration.Settings); err != nil {
			return fmt.Errorf("migration %d %s: %w", migration.Version, migration.Name, err)
		}
	}
	if migration.Run != nil {
		if err := migration.Run(ctx, c); err != nil {
			return fmt.Errorf("migration %d %s: %w", migration.Version, migration.Name, err)
		}
	}
	return nil
}

// Migrate runs the migrations this database has not had yet, one by one,
// recording each. Coroot replicas migrating one project at once take turns
// (ClickhouseMigrationLockId). A database newer than the code (a replica of
// the previous release during a rollout) is left as it is.
func (c *LowLevelClient) Migrate(ctx context.Context, config config.CollectorConfig, database *db.DB, projectId db.ProjectId, migrations []Migration) error {
	return database.WithLock(ctx, db.ClickhouseMigrationLockId, func() error {
		version, err := database.GetClickhouseSchemaVersion(projectId, c.target)
		if err != nil {
			return err
		}
		if latest := len(migrations); version > latest {
			klog.Warningf("ClickHouse schema of project %s (%s) is at version %d, this Coroot knows up to %d: left as it is",
				projectId, c.target, version, latest)
			return nil
		}
		for _, migration := range migrations[version:] {
			if err = c.ExecMigration(ctx, config, migration); err != nil {
				return err
			}
			if err = database.SetClickhouseSchemaVersion(projectId, c.target, migration.Version); err != nil {
				return err
			}
			klog.Infof("ClickHouse schema of project %s (%s): migration %d %s applied", projectId, c.target, migration.Version, migration.Name)
		}
		return nil
	})
}

// Target names the database this client writes to, the key of its schema
// version: a project moved to another ClickHouse or database starts from 0.
func (c *LowLevelClient) Target() string {
	return c.target
}
```

`migrations[version:]` relies on the consecutive versions that `TestMigrationsAreConsecutive` checks.

Logging "once per project" for a newer database: the collector loop calls `Migrate` only until it succeeds (`migrationDone`), so the warning appears once per project per start. Good enough; no extra state.

- [ ] **Step 4: Change `ch/client.go`**

- Add `target string` to `LowLevelClient`.
- In `NewLowLevelClient`, after `c.info(...)` succeeds:

```go
	database := cfg.Database
	if database == "" {
		database = "default"
	}
	c.target = cfg.Addr + "/" + database + "@" + c.cluster
```

- Delete the old `func (c *LowLevelClient) Migrate(ctx context.Context, cfg config.CollectorConfig) error` (lines 184-212). Keep `tables`, `distributedTables`, `timeSeriesTables` and `replacingByVersion`: they are version 1 now. Add above `tables`:

```go
// tables, distributedTables and timeSeriesTables are version 1 of the schema
// (Migrations). Never edit them: a change of the schema is a new Migration.
```

- [ ] **Step 5: Run the unit tests**

Run: `go build ./... ; go test ./ch/`
Expected: `ch` tests PASS; the build fails in `ch/chtest`, `collector/migration.go`, `e2e/storage/migrate_test.go` (old `Migrate`). Fixed in the next steps and in Task 4.

- [ ] **Step 6: Change `ch/chtest/chtest.go`**

Each test database gets a Postgres for its versions. Replace the old migrate call (line 80) with:

```go
	postgres := dbtest.New(t)
	projectId := db.ProjectId(name)
	day := 24 * 3600 * timeseries.Second
	collectorConfig := config.CollectorConfig{TracesTTL: 30 * day, LogsTTL: 30 * day, ProfilesTTL: 30 * day, MetricsTTL: 30 * day}
	if err = ll.Migrate(ctx, collectorConfig, postgres, projectId, ch.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &Env{Config: integration(name), Database: name, LL: ll, Postgres: postgres, ProjectId: projectId, CollectorConfig: collectorConfig}
```

Add the fields `Postgres *db.DB`, `ProjectId db.ProjectId`, `CollectorConfig config.CollectorConfig` to `Env` with a comment: "the Postgres holding the schema version of the database, as Coroot's". Import `github.com/coroot/coroot/db/dbtest`. Check for an import cycle: `dbtest` imports only `db`, so none.

Add a second constructor for tests that need a database with no schema:

```go
// NewEmpty is New without the schema: the database exists, nothing in it.
func NewEmpty(t *testing.T) *Env
```

Implement it by moving everything in `New` before the migrate call into `NewEmpty` (returning the `Env` without migrating) and making `New` call `NewEmpty` and then migrate. That is not a forwarder: `New` adds the migration.

- [ ] **Step 7: Update `e2e/storage/migrate_test.go`**

`TestMigrateAppliesAndReplays`: replace the second `e.LL.Migrate(...)` with

```go
	require.NoError(t, e.LL.Migrate(context.Background(), e.CollectorConfig, e.Postgres, e.ProjectId, ch.Migrations))
```

and drop the now unused `config`/`timeseries` imports if any.

- [ ] **Step 8: Write the failing storage tests** (`e2e/storage/clickhouse_migrations_test.go`)

```go
//go:build e2e

package storage

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/stretchr/testify/require"
)

func createQueries(t *testing.T, e *chtest.Env) []string {
	t.Helper()
	return queryStrings(t, e, "SELECT name || ': ' || create_table_query FROM system.tables WHERE database = currentDatabase()")
}

func schemaVersion(t *testing.T, e *chtest.Env) int {
	t.Helper()
	version, err := e.Postgres.GetClickhouseSchemaVersion(e.ProjectId, e.LL.Target())
	require.NoError(t, err)
	return version
}

func TestClickhouseMigrateEmptyDatabase(t *testing.T) {
	e := chtest.New(t)
	require.Equal(t, len(ch.Migrations), schemaVersion(t, e))
	require.Contains(t, queryStrings(t, e, "SELECT name FROM system.tables WHERE database = currentDatabase()"), "otel_traces")
}

// A database created by the Coroot without versions (version 1 run with no
// record) is recorded at version 1 with no error and no change.
func TestClickhouseBaselineOnAnOldDatabase(t *testing.T) {
	e := chtest.NewEmpty(t)
	ctx := context.Background()
	require.NoError(t, e.LL.ExecMigration(ctx, e.CollectorConfig, ch.Migrations[0]))
	before := createQueries(t, e)

	require.NoError(t, e.LL.Migrate(ctx, e.CollectorConfig, e.Postgres, e.ProjectId, ch.Migrations))
	require.Equal(t, len(ch.Migrations), schemaVersion(t, e))
	require.Equal(t, before, createQueries(t, e))
}

func TestClickhouseMigrateConcurrently(t *testing.T) {
	e := chtest.NewEmpty(t)
	ctx := context.Background()
	var applied sync.Map
	counting := append([]ch.Migration{}, ch.Migrations...)
	counting = append(counting, ch.Migration{
		Version: len(ch.Migrations) + 1,
		Name:    "count",
		Run: func(ctx context.Context, client *ch.LowLevelClient) error {
			count, _ := applied.LoadOrStore("count", new(int))
			*count.(*int)++
			return nil
		},
	})
	var group sync.WaitGroup
	results := make([]error, 2)
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index] = e.LL.Migrate(ctx, e.CollectorConfig, e.Postgres, e.ProjectId, counting)
		}()
	}
	group.Wait()
	require.NoError(t, results[0])
	require.NoError(t, results[1])
	count, _ := applied.Load("count")
	require.Equal(t, 1, *count.(*int), "the migration ran once")
	require.Equal(t, len(counting), schemaVersion(t, e))
}

// A migration failing at its second statement keeps the version; the next pass
// runs it again from the start and finishes.
func TestClickhouseFailedMigrationRunsAgain(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	failing := true
	next := ch.Migration{
		Version: len(ch.Migrations) + 1,
		Name:    "add a column",
		Statements: []string{
			"ALTER TABLE otel_traces @on_cluster ADD COLUMN IF NOT EXISTS TestColumn String",
		},
		Run: func(ctx context.Context, client *ch.LowLevelClient) error {
			if failing {
				return errors.New("failed half way")
			}
			return nil
		},
	}
	migrations := append(append([]ch.Migration{}, ch.Migrations...), next)
	require.Error(t, e.LL.Migrate(ctx, e.CollectorConfig, e.Postgres, e.ProjectId, migrations))
	require.Equal(t, len(ch.Migrations), schemaVersion(t, e), "the version stays")

	failing = false
	require.NoError(t, e.LL.Migrate(ctx, e.CollectorConfig, e.Postgres, e.ProjectId, migrations))
	require.Equal(t, len(migrations), schemaVersion(t, e))
}

// A new migration on a database with data keeps the data.
func TestClickhouseMigrationKeepsData(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	e.Exec(t, "INSERT INTO otel_traces_distributed (Timestamp, TraceId, SpanId, ServiceName) SETTINGS distributed_foreground_insert = 1 VALUES (now(), 't1', 's1', 'svc')")
	migrations := append(append([]ch.Migration{}, ch.Migrations...), ch.Migration{
		Version:    len(ch.Migrations) + 1,
		Name:       "add a column",
		Statements: []string{"ALTER TABLE otel_traces @on_cluster ADD COLUMN IF NOT EXISTS TestColumn String DEFAULT 'x'"},
	})
	require.NoError(t, e.LL.Migrate(ctx, e.CollectorConfig, e.Postgres, e.ProjectId, migrations))
	require.Equal(t, []string{"t1 x"}, queryStrings(t, e, "SELECT TraceId || ' ' || TestColumn FROM otel_traces_distributed"))
}

// A replica of the previous release (it knows fewer migrations than the
// database had) neither fails nor changes anything.
func TestClickhouseOlderCodeKeepsWorking(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	require.NoError(t, e.Postgres.SetClickhouseSchemaVersion(e.ProjectId, e.LL.Target(), len(ch.Migrations)+5))
	require.NoError(t, e.LL.Migrate(ctx, e.CollectorConfig, e.Postgres, e.ProjectId, ch.Migrations))
	require.Equal(t, len(ch.Migrations)+5, schemaVersion(t, e))
}
```

Before running, check the `otel_traces_distributed` insert against the columns in `ch/client.go` (`otel_traces` has more required columns? Columns without DEFAULT in ClickHouse get type defaults, so a partial insert works). The "data" test reads through `otel_traces_distributed`; `queryStrings` uses `clickhouse.NewClient`, which replaces table names — read the query back if the distributed name is rewritten (`ReplaceTables`), and use `otel_traces` if so.

- [ ] **Step 9: Run them**

Run: `make test-e2e RUN='TestClickhouse|TestMigrateAppliesAndReplays'`
Expected: PASS (they fail to compile before steps 3–6).

To see a test catch a real bug once: temporarily change `migrations[version:]` to `migrations` in `Migrate` and run `TestClickhouseMigrateConcurrently` — it must fail with "the migration ran once" (count 2). Revert.

---

### Task 4: The collector migrates through versions

**Files:**
- Modify: `collector/migration.go:67-103`, `collector/collector.go` (only if the call sites need it)
- Test: `e2e/storage/clickhouse_migrations_test.go` (append)

**Interfaces:**
- Consumes: `(*ch.LowLevelClient).Migrate(ctx, config, database, projectId, ch.Migrations)`, `Target()`.

- [ ] **Step 1: Write the failing test**

```go
// A project moved to another ClickHouse database is migrated from 0 there, not
// taken as done because its first database was.
func TestProjectMovedToAnotherDatabaseMigratesFromZero(t *testing.T) {
	first := chtest.New(t)
	second := chtest.NewEmpty(t)
	ctx := context.Background()
	// the same project and the same Postgres, a different database
	require.NoError(t, second.LL.Migrate(ctx, first.CollectorConfig, first.Postgres, first.ProjectId, ch.Migrations))
	version, err := first.Postgres.GetClickhouseSchemaVersion(first.ProjectId, second.LL.Target())
	require.NoError(t, err)
	require.Equal(t, len(ch.Migrations), version)
	require.Contains(t, queryStrings(t, second, "SELECT name FROM system.tables WHERE database = currentDatabase()"), "otel_traces")
}
```

- [ ] **Step 2: Run it**

Run: `make test-e2e RUN=TestProjectMovedToAnotherDatabaseMigratesFromZero`
Expected: PASS already (the target differs by database). It pins the behavior; to see it catch the bug, temporarily make `Target()` return `""` in `NewLowLevelClient` — the test fails (no tables in `second`). Revert.

- [ ] **Step 3: Change `migrateProject`**

Replace the `WithLock` block in `collector/migration.go` with the versioned call (the lock is inside `Migrate` now):

```go
	err = client.Migrate(ctx, c.cfg, c.db, p.Id, ch.Migrations)
	if err == nil {
		c.migrationDoneLock.Lock()
		c.migrationDone[p.Id] = true
		c.migrationDoneLock.Unlock()
	}
	return err
```

Update the comment of `migrateProjects`: the statements are no longer replayed; a project migrated by another replica is found at the latest version and skipped. Remove the comment "one replica at a time..." from `migrateProject` (it moved into `ch.Migrate`).

- [ ] **Step 4: Build and run the collector tests**

Run: `go build ./... && go vet ./... && go test ./collector/ ./ch/ ./db/`
Expected: PASS. `collector/projects_test.go` (config change resets `migrationDone`) still holds.

- [ ] **Step 5: Run every storage test**

Run: `make test-e2e RUN='TestClickhouse|TestPostgres|TestProject|TestMigrate'`
Expected: PASS.

---

### Task 5: Docs, full suite, dev deployment

**Files:**
- Create: `docs/docs/development/schema-migrations.md` — first `ls docs/docs` and put it where developer docs live (if there is no such folder, `docs/schema-migrations.md` at the repository's docs root, not in the user docs sidebar).
- Modify: `docs/superpowers/plans/2026-10-20-multi-replica-followups.md` (section 5)

- [ ] **Step 1: Write the page for migration authors**

Content (in English, the docs' language):

```markdown
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
(`clickhouse_schema`, keyed by project and database). A released migration
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
- **A column type:** `ALTER TABLE ... MODIFY COLUMN` is a mutation that
  rewrites the column in every part; on large tables prefer a new column
  filled by `DEFAULT`/`MATERIALIZED` and switch the reads.
```

- [ ] **Step 2: Add the TTL follow-up**

In `docs/superpowers/plans/2026-10-20-multi-replica-followups.md`, section 5, add:

```markdown
- **TTL of existing ClickHouse tables:** `@ttl_*` is filled from the config
  when a table is created (version 1 of `ch.Migrations`); a changed
  `*_TTL` later does not reach existing tables. Reconcile at the collector's
  migration pass: compare `system.tables.engine_full` with the config and run
  `ALTER TABLE ... @on_cluster MODIFY TTL ...` when they differ.
```

- [ ] **Step 3: Run the whole suite**

Run: `go build ./... && go vet ./... && go test ./... && make test-e2e`
Expected: everything PASS. Note any failure by name in the report, with its output, even if it looks unrelated.

- [ ] **Step 4: Deploy to dev and check the existing databases**

Tilt rebuilds Coroot (2 replicas). Then:

```bash
kubectl --context k3s-server -n coroot-dev logs -l app=coroot --tail=200 | grep -i "migration"
psql "postgres://coroot:coroot@127.0.0.1:15432/coroot?sslmode=disable" -c "SELECT version_id, is_applied FROM goose_db_version ORDER BY id" -c "SELECT * FROM clickhouse_schema"
```

Expected:
- goose versions 1 and 2 applied once (by one replica);
- one `clickhouse_schema` row per project at version 1;
- no `migration` error in either replica's log;
- the Overview, Traces and Logs pages show the data from before the deployment (no data lost): count `otel_traces` rows before and after, the number after is not lower.

- [ ] **Step 5: Leave for the user to commit** (no `git commit`). Report the files changed and the test results.

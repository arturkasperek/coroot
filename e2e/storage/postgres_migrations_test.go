//go:build e2e

package storage

import (
	"sync"
	"testing"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/db/dbtest"
	"github.com/stretchr/testify/require"
)

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
	database := dbtest.Open(t, dbtest.NewEmpty(t))
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
	database := dbtest.Open(t, dbtest.NewEmpty(t))
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
	dsn := dbtest.NewEmpty(t)
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
	require.NoError(t, database.SaveProject(project))
	require.NoError(t, database.SetClickhouseSchemaVersion(project.Id, "ch:9000/coroot_x@coroot", 1))
	require.NoError(t, database.DeleteProject(project.Id))
	version, err := database.GetClickhouseSchemaVersion(project.Id, "ch:9000/coroot_x@coroot")
	require.NoError(t, err)
	require.Equal(t, 0, version)
}

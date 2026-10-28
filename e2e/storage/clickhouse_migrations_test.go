//go:build e2e

package storage

import (
	"context"
	"errors"
	"net"
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
		Statements: []string{
			"ALTER TABLE otel_traces @on_cluster ADD COLUMN IF NOT EXISTS TestColumn String DEFAULT 'x'",
			// a Distributed table keeps the columns it was created with
			"ALTER TABLE otel_traces_distributed @on_cluster ADD COLUMN IF NOT EXISTS TestColumn String DEFAULT 'x'",
		},
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

// A database dropped and created again (a wiped ClickHouse volume) gets its
// tables back, though Postgres still holds its version.
func TestClickhouseRecreatedDatabaseGetsItsTablesBack(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	adminConfig := *e.Config
	adminConfig.Database = "default"
	admin, err := ch.NewLowLevelClient(ctx, &adminConfig)
	require.NoError(t, err)
	defer admin.Close()
	require.NoError(t, admin.Exec(ctx, "DROP DATABASE "+e.Database+" @on_cluster SYNC"))
	require.NoError(t, admin.CreateDB(ctx, e.Database))

	require.NoError(t, e.LL.Migrate(ctx, e.CollectorConfig, e.Postgres, e.ProjectId, ch.Migrations))
	require.Contains(t, queryStrings(t, e, "SELECT name FROM system.tables WHERE database = currentDatabase()"), "otel_traces")
	require.Equal(t, len(ch.Migrations), schemaVersion(t, e))
}

// The version belongs to the database, not to the address it is reached by.
func TestClickhouseTargetDoesNotDependOnTheAddress(t *testing.T) {
	e := chtest.NewEmpty(t)
	ctx := context.Background()
	otherAddress := *e.Config
	host, port, err := net.SplitHostPort(otherAddress.Addr)
	require.NoError(t, err)
	if host == "127.0.0.1" {
		host = "localhost"
	} else {
		host = "127.0.0.1"
	}
	otherAddress.Addr = net.JoinHostPort(host, port)
	other, err := ch.NewLowLevelClient(ctx, &otherAddress)
	require.NoError(t, err)
	defer other.Close()
	require.Equal(t, e.LL.Target(), other.Target())
}

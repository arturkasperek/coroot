package ch

import (
	"context"
	"fmt"
	"strings"

	chgo "github.com/ClickHouse/ch-go"
	chproto "github.com/ClickHouse/ch-go/proto"
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
		if version > 0 {
			exists, err := c.tableExists(ctx, schemaSentinelTable)
			if err != nil {
				return err
			}
			if !exists {
				klog.Warningf("ClickHouse schema of project %s (%s) is recorded at version %d, but the database has no %s: migrating it from the start",
					projectId, c.target, version, schemaSentinelTable)
				version = 0
			}
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

// schemaSentinelTable is a table every version of the schema has. A database
// without it was dropped or created anew after its version was recorded (a
// wiped ClickHouse), and is migrated from the start.
const schemaSentinelTable = "otel_traces"

func (c *LowLevelClient) tableExists(ctx context.Context, table string) (bool, error) {
	var exists chproto.ColUInt8
	err := c.pool.Do(ctx, chgo.Query{
		Body:   "EXISTS TABLE " + table,
		Result: chproto.Results{{Name: "result", Data: &exists}},
	})
	if err != nil {
		return false, err
	}
	return exists.Row(0) == 1, nil
}

// Target names the database this client writes to, the key of its schema
// version: the database and the cluster, not the address, so that replicas
// reaching one ClickHouse by different names share the version. A project
// moved to another database starts from 0; one moved to another ClickHouse
// with the same database name is found without its tables and starts from 0
// too (schemaSentinelTable).
func (c *LowLevelClient) Target() string {
	return c.target
}

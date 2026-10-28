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

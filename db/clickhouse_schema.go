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

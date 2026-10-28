package db

import (
	"database/sql"
	"errors"
	"time"
)

// mcpSessionMaxAge is how long the project selected in an MCP session is
// kept after it was last selected.
const mcpSessionMaxAge = 30 * 24 * time.Hour

// The project selected in an MCP session is kept in the database (the table
// mcp_session), not in memory, so that the calls of a session can go to any
// Coroot replica.

// SetMcpSessionProject selects a project for an MCP session, and forgets the
// sessions not used for mcpSessionMaxAge.
func (db *DB) SetMcpSessionProject(sessionId string, projectId ProjectId) error {
	now := time.Now()
	if _, err := db.db.Exec("DELETE FROM mcp_session WHERE updated_at < $1", now.Add(-mcpSessionMaxAge).Unix()); err != nil {
		return err
	}
	_, err := db.db.Exec(`
		INSERT INTO mcp_session (id, project_id, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET project_id = EXCLUDED.project_id, updated_at = EXCLUDED.updated_at`,
		sessionId, projectId, now.Unix())
	return err
}

// GetMcpSessionProject is the project selected for an MCP session, "" when
// none is.
func (db *DB) GetMcpSessionProject(sessionId string) (ProjectId, error) {
	var projectId ProjectId
	err := db.db.QueryRow("SELECT project_id FROM mcp_session WHERE id = $1", sessionId).Scan(&projectId)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return projectId, err
}

// DeleteMcpSession forgets an MCP session that ended.
func (db *DB) DeleteMcpSession(sessionId string) error {
	_, err := db.db.Exec("DELETE FROM mcp_session WHERE id = $1", sessionId)
	return err
}

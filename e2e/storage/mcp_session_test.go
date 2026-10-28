//go:build e2e

package storage

import (
	"testing"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The project selected in an MCP session on one Coroot replica is the one the
// calls of the session use on another.
func TestMcpSessionProjectIsSharedByReplicas(t *testing.T) {
	one, dsn := dbtest.NewWithDSN(t)
	other := dbtest.Open(t, dsn)

	selected, err := other.GetMcpSessionProject("session-1")
	require.NoError(t, err)
	assert.Equal(t, db.ProjectId(""), selected, "nothing selected yet")

	require.NoError(t, one.SetMcpSessionProject("session-1", "project-a"))
	selected, err = other.GetMcpSessionProject("session-1")
	require.NoError(t, err)
	assert.Equal(t, db.ProjectId("project-a"), selected)

	require.NoError(t, other.SetMcpSessionProject("session-1", "project-b"))
	selected, err = one.GetMcpSessionProject("session-1")
	require.NoError(t, err)
	assert.Equal(t, db.ProjectId("project-b"), selected, "selected again on the other replica")

	require.NoError(t, one.DeleteMcpSession("session-1"))
	selected, err = other.GetMcpSessionProject("session-1")
	require.NoError(t, err)
	assert.Equal(t, db.ProjectId(""), selected, "an ended session selects nothing")
}

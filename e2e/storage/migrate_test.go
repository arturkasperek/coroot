//go:build e2e

package storage

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/db"
	"github.com/stretchr/testify/require"
)

func queryStrings(t *testing.T, e *chtest.Env, query string) []string {
	t.Helper()
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()
	rows, err := c.Query(context.Background(), query)
	require.NoError(t, err)
	defer rows.Close()
	var res []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		res = append(res, s)
	}
	sort.Strings(res)
	return res
}

// The whole schema applies to the ClickHouse version the product ships, and
// version 1 run again (as on a database created before versions, or after a
// migration interrupted half way) changes nothing and fails nowhere.
func TestMigrateAppliesAndReplays(t *testing.T) {
	e := chtest.New(t)
	before := queryStrings(t, e, "SELECT name FROM system.tables WHERE database = currentDatabase()")
	require.Contains(t, before, "otel_logs")
	require.Contains(t, before, "otel_traces")

	require.NoError(t, e.LL.ExecMigration(context.Background(), e.CollectorConfig, ch.Migrations[0]))
	after := queryStrings(t, e, "SELECT name FROM system.tables WHERE database = currentDatabase()")
	require.Equal(t, before, after)

	version := queryStrings(t, e, "SELECT version()")
	t.Logf("ClickHouse %s, %d tables: %s", version[0], len(after), strings.Join(after, " "))
}

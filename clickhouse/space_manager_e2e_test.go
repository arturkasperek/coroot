//go:build e2e

package clickhouse

import (
	"context"
	"fmt"
	"testing"

	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/config"
	"github.com/coroot/coroot/db"
	"github.com/stretchr/testify/require"
)

// insertSpans writes one span per day offset, each INSERT a part of its own.
func insertSpans(t *testing.T, environment *chtest.Env, dayOffsets ...int) {
	t.Helper()
	for index, dayOffset := range dayOffsets {
		environment.Exec(t, fmt.Sprintf(
			"INSERT INTO otel_traces (Timestamp, TraceId, ServiceName, SpanName) VALUES (now() - INTERVAL %d DAY, 'trace-%d', 'service', 'span')",
			dayOffset, index))
	}
}

func partitionsOf(t *testing.T, client *Client, database, table string) []string {
	t.Helper()
	rows, err := client.conn.Query(context.Background(),
		"SELECT DISTINCT partition_id FROM system.parts WHERE active AND database = ? AND table = ? ORDER BY partition_id",
		database, table)
	require.NoError(t, err)
	defer rows.Close()
	var partitions []string
	for rows.Next() {
		var partition string
		require.NoError(t, rows.Scan(&partition))
		partitions = append(partitions, partition)
	}
	require.NoError(t, rows.Err())
	return partitions
}

func newTestSpaceManager(t *testing.T, environment *chtest.Env) (*SpaceManager, *Client) {
	t.Helper()
	client, err := NewClient(environment.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	spaceManager := &SpaceManager{
		client:    client,
		cfg:       config.ClickHouseSpaceManager{Enabled: true, MinPartitions: 1},
		databases: []string{environment.Database},
	}
	return spaceManager, client
}

// A partition is made of parts: one per INSERT until ClickHouse merges them.
// The minimum to keep counts partitions, so the only partition of a table
// survives however many parts it has.
func TestSpaceManagerKeepsTheOnlyPartitionOfManyParts(t *testing.T) {
	environment := chtest.New(t)
	environment.Exec(t, "SYSTEM STOP MERGES otel_traces")
	insertSpans(t, environment, 0, 0, 0)
	spaceManager, client := newTestSpaceManager(t, environment)
	partitionsBefore := partitionsOf(t, client, environment.Database, "otel_traces")
	require.Len(t, partitionsBefore, 1)

	require.NoError(t, spaceManager.cleanupOldestPartitionsFromDisk(context.Background(), client, "default"))

	require.Equal(t, partitionsBefore, partitionsOf(t, client, environment.Database, "otel_traces"))
}

func TestSpaceManagerDropsTheOldestOfSeveralPartitions(t *testing.T) {
	environment := chtest.New(t)
	environment.Exec(t, "SYSTEM STOP MERGES otel_traces")
	insertSpans(t, environment, 1, 1, 0, 0)
	spaceManager, client := newTestSpaceManager(t, environment)
	partitionsBefore := partitionsOf(t, client, environment.Database, "otel_traces")
	require.Len(t, partitionsBefore, 2)

	require.NoError(t, spaceManager.cleanupOldestPartitionsFromDisk(context.Background(), client, "default"))

	require.Equal(t, partitionsBefore[1:], partitionsOf(t, client, environment.Database, "otel_traces"))
}

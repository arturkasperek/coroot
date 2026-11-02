//go:build e2e

package clickhouse

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/coroot/coroot/ch"
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

// The samples of the metrics TimeSeries table live in its inner table
// `.inner_id.samples.<uuid>`, partitioned by 5 hours, and the bodies of the
// traces in trace_bodies: both are dropped by partition like the otel_ tables.
// The inner tags table is not partitioned by time and is left alone.
func TestSpaceManagerDropsTheOldestPartitionOfMetricSamplesAndTraceBodies(t *testing.T) {
	environment := chtest.New(t)
	spaceManager, client := newTestSpaceManager(t, environment)
	var samplesTable, tagsTable string
	require.NoError(t, client.conn.QueryRow(context.Background(),
		"SELECT name FROM system.tables WHERE database = currentDatabase() AND name LIKE '.inner_id.samples.%'").Scan(&samplesTable))
	require.NoError(t, client.conn.QueryRow(context.Background(),
		"SELECT name FROM system.tables WHERE database = currentDatabase() AND name LIKE '.inner_id.tags.%'").Scan(&tagsTable))
	now := time.Now().UTC().Unix()
	require.NoError(t, environment.LL.ExecWithSettings(context.Background(), fmt.Sprintf(
		"INSERT INTO metrics (metric_name, tags, samples) VALUES ('up', map('instance', 'i1'), [(toDateTime64(%d, 3), 1.0), (toDateTime64(%d, 3), 1.0)])",
		now-10*3600, now), ch.TimeSeriesSettings))
	for _, daysAgo := range []int{1, 0} {
		environment.Exec(t, fmt.Sprintf(
			"INSERT INTO trace_bodies (Hash, Body, LastSeen) VALUES (%d, 'body', now() - INTERVAL %d DAY)", daysAgo, daysAgo))
	}
	require.Eventually(t, func() bool {
		return len(partitionsOf(t, client, environment.Database, samplesTable)) == 2
	}, 20*time.Second, 200*time.Millisecond)
	samplesBefore := partitionsOf(t, client, environment.Database, samplesTable)
	require.Len(t, samplesBefore, 2)
	bodiesBefore := partitionsOf(t, client, environment.Database, "trace_bodies")
	require.Len(t, bodiesBefore, 2)
	tagsBefore := partitionsOf(t, client, environment.Database, tagsTable)
	require.NotEmpty(t, tagsBefore)

	require.NoError(t, spaceManager.cleanupOldestPartitionsFromDisk(context.Background(), client, "default"))

	require.Equal(t, samplesBefore[1:], partitionsOf(t, client, environment.Database, samplesTable))
	require.Equal(t, bodiesBefore[1:], partitionsOf(t, client, environment.Database, "trace_bodies"))
	require.Equal(t, tagsBefore, partitionsOf(t, client, environment.Database, tagsTable))
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

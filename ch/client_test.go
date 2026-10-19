package ch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tableSQLContaining(substr string) string {
	for _, s := range tables {
		if strings.Contains(s, substr) {
			return s
		}
	}
	return ""
}

func TestMaterializedColumnsOnCreateTable(t *testing.T) {
	joined := strings.Join(tables, "\n")
	assert.NotContains(t, joined, "ALTER TABLE otel_logs")
	assert.NotContains(t, joined, "ADD COLUMN IF NOT EXISTS Namespace")
	assert.NotContains(t, joined, "ADD COLUMN IF NOT EXISTS Application")
	assert.NotContains(t, joined, "ADD COLUMN IF NOT EXISTS ApiRoute")

	logs := tableSQLContaining("CREATE TABLE IF NOT EXISTS otel_logs @on_cluster")
	require.NotEmpty(t, logs)
	assert.Contains(t, logs, "Namespace LowCardinality(String) MATERIALIZED")
	assert.Contains(t, logs, "Application LowCardinality(String) MATERIALIZED")
	assert.Contains(t, logs, "startsWith(ServiceName, '/k8s')")
	assert.Contains(t, logs, "k8s.namespace.name")

	traces := tableSQLContaining("CREATE TABLE IF NOT EXISTS otel_traces @on_cluster")
	require.NotEmpty(t, traces)
	// the namespace of a span is the one of its Server, derived when read
	assert.NotContains(t, traces, "Namespace LowCardinality(String) MATERIALIZED")
	assert.Contains(t, traces, "Client LowCardinality(String)")
	assert.Contains(t, traces, "Server LowCardinality(String)")
	assert.Contains(t, traces, "ORDER BY (Server, ServiceName,")
	assert.Contains(t, traces, "ApiRoute LowCardinality(String) MATERIALIZED")
	assert.NotContains(t, traces, "LogAttributes")
	assert.Contains(t, traces, "substringIndex(")
	assert.Contains(t, traces, "char(63)")
	assert.NotContains(t, traces, "?")
	assert.NotContains(t, traces, "[1]")
	assert.Greater(t, strings.Index(traces, "http.target"), strings.Index(traces, "http.route"))
}

func TestTraceBodyColumns(t *testing.T) {
	traces := tableSQLContaining("CREATE TABLE IF NOT EXISTS otel_traces @on_cluster")
	require.NotEmpty(t, traces)
	// the spans keep the hashes of their bodies, the bodies are in trace_bodies
	assert.Contains(t, traces, "RequestBodyHash UInt64")
	assert.Contains(t, traces, "ResponseBodyHash UInt64")
	assert.NotContains(t, traces, "RequestBody String")
	bodies := tableSQLContaining("CREATE TABLE IF NOT EXISTS trace_bodies @on_cluster")
	require.NotEmpty(t, bodies)
	assert.Contains(t, bodies, "INDEX idx_body Body TYPE text(tokenizer = 'splitByNonAlpha') GRANULARITY 1")
	assert.Contains(t, bodies, "@replacing_merge_tree_by(LastSeen)")
	assert.Contains(t, bodies, "TTL LastSeen + ")
	assert.Contains(t, strings.Join(distributedTables, "\n"), "Distributed(@cluster, currentDatabase(), trace_bodies, Hash)")

	// no migration statements for them, and never materialized out of the map:
	// that would keep the body in SpanAttributes
	joined := strings.Join(tables, "\n") + strings.Join(distributedTables, "\n")
	assert.NotContains(t, joined, "ADD COLUMN IF NOT EXISTS RequestBody")
	assert.NotContains(t, joined, "MATERIALIZED SpanAttributes['http.request.body']")

	// the Distributed table is created AS otel_traces after the local one, so it
	// copies the columns; make sure that ordering does not change
	dist := strings.Join(distributedTables, "\n")
	assert.Contains(t, dist, "CREATE TABLE IF NOT EXISTS otel_traces_distributed ON CLUSTER @cluster AS otel_traces")
}

// The log search in clickhouse/logs.go (hasAllTokens over lowerUTF8(Body)) is
// served by this index only when both use the same expression.
func TestLogsBodyTextIndex(t *testing.T) {
	logs := tableSQLContaining("CREATE TABLE IF NOT EXISTS otel_logs @on_cluster")
	require.NotEmpty(t, logs)
	assert.Contains(t, logs, "INDEX idx_body lowerUTF8(Body) TYPE text(tokenizer = 'splitByNonAlpha') GRANULARITY 1")
	assert.NotContains(t, logs, "tokenbf_v1")
}

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
	assert.Contains(t, traces, "Namespace LowCardinality(String) MATERIALIZED")
	assert.Contains(t, traces, "ApiRoute LowCardinality(String) MATERIALIZED")
	assert.Contains(t, traces, "ResourceAttributes['k8s.namespace.name']")
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
	assert.Contains(t, traces, "RequestBody String CODEC(ZSTD(1))")
	assert.Contains(t, traces, "ResponseBody String CODEC(ZSTD(1))")
	assert.Contains(t, traces, "INDEX idx_request_body RequestBody TYPE text(tokenizer = 'splitByNonAlpha') GRANULARITY 1")
	assert.Contains(t, traces, "INDEX idx_response_body ResponseBody TYPE text(tokenizer = 'splitByNonAlpha') GRANULARITY 1")

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

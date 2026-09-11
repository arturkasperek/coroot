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

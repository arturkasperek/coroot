package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONEachRow_EncodesAgentLog(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 123456789, time.UTC)
	r := Record(Config{Days: 1, Count: 1}, now, 0)
	line, err := encodeJSONEachRow(r)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(line, &got))
	assert.Equal(t, "2026-09-08 12:00:00.123456789", got["Timestamp"])
	assert.Equal(t, r.ServiceName, got["ServiceName"])
	assert.Equal(t, r.Body, got["Body"])
	assert.Equal(t, r.SeverityText, got["SeverityText"])
	assert.Equal(t, float64(r.SeverityNumber), got["SeverityNumber"])
	assert.Equal(t, "1", got["LogAttributes"].(map[string]any)["chseed"])
}

func TestJSONEachRow_EncodesOtelTrace(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 123456789, time.UTC)
	r := TraceRecordFor(Config{Days: 1, Count: 1}, now, 0)
	line, err := encodeTraceJSONEachRow(r)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(line, &got))
	assert.Equal(t, "2026-09-08 12:00:00.123456789", got["Timestamp"])
	assert.Equal(t, r.ServiceName, got["ServiceName"])
	assert.Equal(t, r.SpanName, got["SpanName"])
	assert.Equal(t, r.SpanKind, got["SpanKind"])
	assert.Equal(t, r.StatusCode, got["StatusCode"])
	assert.Equal(t, float64(r.Duration), got["Duration"])
	assert.Equal(t, "1", got["SpanAttributes"].(map[string]any)["chseed"])
	assert.Equal(t, "coroot-dev", got["ResourceAttributes"].(map[string]any)["k8s.namespace.name"])
}

func TestClickHouseQueryURL(t *testing.T) {
	u, err := clickHouseQueryURL("http://127.0.0.1:18123", "coroot_abc", "SHOW DATABASES")
	require.NoError(t, err)
	assert.Equal(t, "http", u.Scheme)
	assert.Equal(t, "127.0.0.1:18123", u.Host)
	assert.Equal(t, "coroot_abc", u.Query().Get("database"))
	assert.Equal(t, "SHOW DATABASES", u.Query().Get("query"))
}

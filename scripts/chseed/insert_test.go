package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraceCleanupQueries_TargetSeedRowsOnly(t *testing.T) {
	qs := traceCleanupQueries()
	require.Len(t, qs, 3)
	assert.Contains(t, qs[0], "otel_traces")
	assert.Contains(t, qs[0], "SpanAttributes['chseed']")
	assert.Contains(t, qs[1], "otel_traces_histogram")
	assert.Contains(t, qs[1], "NetPeerName = 'chseed'")
	assert.Contains(t, qs[2], "otel_traces_trace_id_ts")
	assert.Contains(t, qs[2], "startsWith(TraceId, 'c5eed')")
}

func TestTraceTTLQueries_CoverTraceTables(t *testing.T) {
	qs := traceTTLQueries(2678400)
	require.Len(t, qs, 4)
	joined := qs[0] + qs[1] + qs[2] + qs[3]
	assert.Contains(t, joined, "otel_traces ")
	assert.Contains(t, joined, "otel_traces_histogram")
	assert.Contains(t, joined, "otel_traces_service_name")
	assert.Contains(t, joined, "otel_traces_trace_id_ts")
}

func TestInsertProgressLabelsKind(t *testing.T) {
	assert.Equal(t, "inserted 10000 / 7000000 traces (166ms)", insertProgress("traces", 10000, 7000000, 166*time.Millisecond))
	assert.Equal(t, "inserted 20000 / 7000000 logs (329ms)", insertProgress("logs", 20000, 7000000, 329*time.Millisecond))
}

package clickhouse

import (
	"strings"
	"testing"

	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpanQueryFilterSkipsNamedField(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("ServiceName", "=", "express-demo")
	q.AddFilter("SpanName", "=", "GET /api/hello")

	filter, _ := q.filter("ServiceName")
	joined := strings.Join(filter, " AND ")
	assert.NotContains(t, joined, "ServiceName")
	assert.Contains(t, joined, "SpanName")

	filter, _ = q.filter("SpanName")
	joined = strings.Join(filter, " AND ")
	assert.Contains(t, joined, "ServiceName")
	assert.NotContains(t, joined, "SpanName")
}

func TestTraceFacetSelectSQL(t *testing.T) {
	q, ok := traceFacetSelectSQL("ServiceName", false)
	require.True(t, ok)
	assert.Contains(t, q, "SELECT ServiceName, count(1)")
	assert.Contains(t, q, "@@table_otel_traces@@")
	assert.Contains(t, q, "HAVING ServiceName != ''")
	assert.Contains(t, q, "LIMIT 1000")

	q, ok = traceFacetSelectSQL("SpanName", true)
	require.True(t, ok)
	assert.Contains(t, q, "SELECT SpanName, sum(Total)")
	assert.Contains(t, q, "@@table_otel_traces_histogram@@")

	_, ok = traceFacetSelectSQL("TraceId", false)
	assert.False(t, ok)
}

func TestBuildTraceFacetQueryExcludesOwnField(t *testing.T) {
	q := SpanQuery{
		Ctx:    timeseries.NewContext(1_700_000_000, 1_700_003_600, 15),
		TsFrom: 1_700_000_000,
		TsTo:   1_700_003_600,
	}
	q.AddFilter("ServiceName", "=", "flask-demo")
	q.AddFilter("SpanName", "=", "GET /api/hello")

	query, _, ok := buildTraceFacetQuery(q, "ServiceName", false)
	require.True(t, ok)
	assert.Contains(t, query, "ParentSpanId = ''")
	assert.Contains(t, query, "NOT startsWith(ServiceName, '/')")
	assert.Contains(t, query, "SpanName")
	assert.NotContains(t, query, "ServiceName =")
}

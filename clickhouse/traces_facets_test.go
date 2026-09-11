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

func TestTraceNamespaceExprUsesResourceAttribute(t *testing.T) {
	expr := traceNamespaceExpr()
	assert.Contains(t, expr, "ResourceAttributes['k8s.namespace.name']")
	assert.Contains(t, expr, "'n/a'")
	assert.NotContains(t, expr, "LogAttributes")
}

func TestSpanQueryFilterNamespaceUsesExprNotColumn(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("Namespace", "=", "coroot-dev")
	q.AddFilter("ServiceName", "=", "express-demo")

	filter, args := q.filter("")
	joined := strings.Join(filter, " AND ")
	assert.Contains(t, joined, traceNamespaceExpr())
	assert.NotContains(t, joined, "Namespace =")
	assert.Contains(t, joined, "ServiceName =")
	assertArgsContain(t, args, "coroot-dev")

	filter, _ = q.filter("Namespace")
	joined = strings.Join(filter, " AND ")
	assert.NotContains(t, joined, traceNamespaceExpr())
	assert.Contains(t, joined, "ServiceName =")

	q.Filters = []SpanFilter{{Field: "Namespace", Op: "!=", Value: "n/a"}}
	filter, _ = q.filter("")
	assert.Contains(t, strings.Join(filter, " AND "), "NOT (")
}

func TestFiltersOnHistogramDimensionsRejectsNamespace(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("Namespace", "=", "coroot-dev")
	assert.False(t, q.filtersOnHistogramDimensions())

	q = SpanQuery{}
	q.AddFilter("ServiceName", "=", "express-demo")
	assert.True(t, q.filtersOnHistogramDimensions())
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

	q, ok = traceFacetSelectSQL("Namespace", false)
	require.True(t, ok)
	assert.Contains(t, q, traceNamespaceExpr())
	assert.Contains(t, q, "@@table_otel_traces@@")
	assert.NotContains(t, q, "@@table_otel_traces_histogram@@")

	_, ok = traceFacetSelectSQL("Namespace", true)
	assert.False(t, ok)

	q, ok = traceFacetSelectSQL("ApiRoute", false)
	require.True(t, ok)
	assert.Contains(t, q, traceApiRouteExpr())
	assert.Contains(t, q, "@@table_otel_traces@@")
	assert.NotContains(t, q, "@@table_otel_traces_histogram@@")

	_, ok = traceFacetSelectSQL("ApiRoute", true)
	assert.False(t, ok)
}

func TestTraceApiRouteExprMatchesColumnFormat(t *testing.T) {
	expr := traceApiRouteExpr()
	assert.Contains(t, expr, "SpanAttributes['http.method']")
	assert.Contains(t, expr, "SpanAttributes['http.request.method']")
	assert.Contains(t, expr, "SpanAttributes['http.route']")
	assert.Contains(t, expr, "SpanAttributes['http.target']")
	assert.Contains(t, expr, "substringIndex(")
	assert.Contains(t, expr, "char(63)")
	assert.NotContains(t, expr, "?")
	assert.NotContains(t, expr, "[1]")
	routeIdx := strings.Index(expr, "http.route")
	targetIdx := strings.Index(expr, "http.target")
	assert.Greater(t, targetIdx, routeIdx)
}

func TestSpanQueryFilterApiRouteUsesExprNotColumn(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("ApiRoute", "=", "GET /health")
	q.AddFilter("ServiceName", "=", "express-demo")

	filter, args := q.filter("")
	joined := strings.Join(filter, " AND ")
	assert.Contains(t, joined, traceApiRouteExpr())
	assert.NotContains(t, joined, "ApiRoute =")
	assert.Contains(t, joined, "ServiceName =")
	assertArgsContain(t, args, "GET /health")

	filter, _ = q.filter("ApiRoute")
	joined = strings.Join(filter, " AND ")
	assert.NotContains(t, joined, traceApiRouteExpr())
	assert.Contains(t, joined, "ServiceName =")
}

func TestFiltersOnHistogramDimensionsRejectsApiRoute(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("ApiRoute", "=", "GET /health")
	assert.False(t, q.filtersOnHistogramDimensions())
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

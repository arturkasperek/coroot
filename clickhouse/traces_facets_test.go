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

func TestSpanQueryFilterNamespaceUsesColumn(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("Namespace", "=", "coroot-dev")
	q.AddFilter("ServiceName", "=", "express-demo")

	filter, args := q.filter("")
	joined := strings.Join(filter, " AND ")
	assert.Contains(t, joined, "Namespace")
	assert.NotContains(t, joined, "ResourceAttributes")
	assert.Contains(t, joined, "ServiceName =")
	assertArgsContain(t, args, "coroot-dev")

	filter, _ = q.filter("Namespace")
	joined = strings.Join(filter, " AND ")
	assert.NotContains(t, joined, "Namespace")
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
	assert.Contains(t, q, "SELECT Namespace AS Namespace")
	assert.NotContains(t, q, "ResourceAttributes")
	assert.Contains(t, q, "@@table_otel_traces@@")
	assert.NotContains(t, q, "@@table_otel_traces_histogram@@")

	_, ok = traceFacetSelectSQL("Namespace", true)
	assert.False(t, ok)

	q, ok = traceFacetSelectSQL("ApiRoute", false)
	require.True(t, ok)
	assert.Contains(t, q, "SELECT ApiRoute AS ApiRoute")
	assert.NotContains(t, q, "SpanAttributes")
	assert.Contains(t, q, "@@table_otel_traces@@")
	assert.NotContains(t, q, "@@table_otel_traces_histogram@@")

	_, ok = traceFacetSelectSQL("ApiRoute", true)
	assert.False(t, ok)
}

func TestSpanQueryFilterApiRouteUsesColumn(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("ApiRoute", "=", "GET /health")
	q.AddFilter("ServiceName", "=", "express-demo")

	filter, args := q.filter("")
	joined := strings.Join(filter, " AND ")
	assert.Contains(t, joined, "ApiRoute")
	assert.NotContains(t, joined, "SpanAttributes")
	assert.Contains(t, joined, "ServiceName =")
	assertArgsContain(t, args, "GET /health")

	filter, _ = q.filter("ApiRoute")
	joined = strings.Join(filter, " AND ")
	assert.NotContains(t, joined, "ApiRoute")
	assert.Contains(t, joined, "ServiceName =")
}

func TestFiltersOnHistogramDimensionsRejectsApiRoute(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("ApiRoute", "=", "GET /health")
	assert.False(t, q.filtersOnHistogramDimensions())
}

func TestTraceFacetCountsStayOnOtelTracesWhenHistogramExists(t *testing.T) {
	q := SpanQuery{
		Ctx:    timeseries.NewContext(1_700_000_000, 1_700_003_600, 15),
		TsFrom: 1_700_000_000,
		TsTo:   1_700_003_600,
	}
	for _, field := range []string{"ServiceName", "SpanName", "Namespace", "ApiRoute"} {
		query, _, ok := buildTraceFacetQuery(q, field, false)
		require.True(t, ok, field)
		assert.Contains(t, query, "@@table_otel_traces@@", field)
		assert.NotContains(t, query, "@@table_otel_traces_histogram@@", field)
	}
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
	assert.NotContains(t, query, "startsWith(ServiceName", "spans of the node-agent are listed too")
	assert.Contains(t, query, "SpanName")
	assert.NotContains(t, query, "ServiceName =")
}

func TestTraceSourceFacet(t *testing.T) {
	q := SpanQuery{
		Ctx:    timeseries.NewContext(1_700_000_000, 1_700_003_600, 15),
		TsFrom: 1_700_000_000,
		TsTo:   1_700_003_600,
	}
	t.Run("the facet tells agent spans from OpenTelemetry spans", func(t *testing.T) {
		query, _, ok := buildTraceFacetQuery(q, "Source", false)
		require.True(t, ok)
		assert.Contains(t, query, "SELECT if(startsWith(ServiceName, '/'), 'agent', 'otel') AS Source, count(1) FROM @@table_otel_traces@@")
		assert.Contains(t, query, "ParentSpanId = ''")
	})
	t.Run("it does not filter on itself", func(t *testing.T) {
		q := q
		q.AddFilter("Source", "=", "agent")
		q.AddFilter("SpanName", "=", "GET /x")
		query, _, ok := buildTraceFacetQuery(q, "Source", false)
		require.True(t, ok)
		assert.NotContains(t, query, "= @filter_0")
		assert.Contains(t, query, "SpanName")
	})
	t.Run("filters on it", func(t *testing.T) {
		for op, want := range map[string]string{
			"=":  "(if(startsWith(ServiceName, '/'), 'agent', 'otel')) = @filter_0",
			"!=": "NOT ((if(startsWith(ServiceName, '/'), 'agent', 'otel')) = @filter_0)",
			"~":  "match(if(startsWith(ServiceName, '/'), 'agent', 'otel'), @filter_0)",
		} {
			q := SpanQuery{}
			q.AddFilter("Source", op, "agent")
			filter, args := q.Filter()
			require.Len(t, filter, 1, op)
			assert.Equal(t, want, filter[0], op)
			require.Len(t, args, 1)
		}
	})
	t.Run("the histogram can still come from the minute table", func(t *testing.T) {
		q := SpanQuery{}
		q.AddFilter("Source", "=", "otel")
		assert.True(t, q.filtersOnHistogramDimensions())
		q.AddFilter("Namespace", "=", "prod")
		assert.False(t, q.filtersOnHistogramDimensions())
	})
}

func TestRootSpansFilterKeepsAgentSpans(t *testing.T) {
	q := SpanQuery{}
	for _, fromMV := range []bool{false, true} {
		filter, _ := q.RootSpansFilter(fromMV)
		assert.NotContains(t, strings.Join(filter, " AND "), "startsWith(ServiceName", "fromMV=%v", fromMV)
	}
}

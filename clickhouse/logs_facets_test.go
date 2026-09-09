package clickhouse

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogQuery() LogQuery {
	return LogQuery{
		Ctx: timeseries.NewContext(1_700_000_000, 1_700_003_600, 15),
		Filters: []LogFilter{
			{Name: "Severity", Op: "=", Value: "error"},
			{Name: "service.name", Op: "=", Value: "/k8s/coroot-dev/express-demo"},
			{Name: "host.name", Op: "=", Value: "node-a"},
			{Name: "e2e.facets", Op: "=", Value: "token"},
		},
	}
}

func assertArgsContain(t *testing.T, args []any, substr string) {
	for _, a := range args {
		if strings.Contains(fmt.Sprint(a), substr) {
			return
		}
	}
	t.Errorf("args do not contain %q", substr)
}

func TestLogQueryFiltersSourceAgentAndOtel(t *testing.T) {
	q := LogQuery{
		Ctx: timeseries.NewContext(1_700_000_000, 1_700_003_600, 15),
		Filters: []LogFilter{
			{Name: "Source", Op: "=", Value: "agent"},
			{Name: "Severity", Op: "=", Value: "error"},
		},
	}
	where, _ := q.filters(nil)
	joined := strings.Join(where, " AND ")
	assert.Contains(t, joined, "startsWith(ServiceName, '/')")
	assert.NotContains(t, joined, "NOT startsWith(ServiceName, '/')")
	assert.Contains(t, joined, "SeverityNumber")

	q.Filters = []LogFilter{{Name: "Source", Op: "=", Value: "otel"}}
	where, _ = q.filters(nil)
	joined = strings.Join(where, " AND ")
	assert.Contains(t, joined, "NOT startsWith(ServiceName, '/')")

	q.Filters = []LogFilter{{Name: "Source", Op: "!=", Value: "agent"}}
	where, _ = q.filters(nil)
	assert.Contains(t, strings.Join(where, " AND "), "NOT (startsWith(ServiceName, '/'))")

	where, _ = q.filters(strPtr("Source"))
	joined = strings.Join(where, " AND ")
	assert.NotContains(t, joined, "startsWith(ServiceName")
}

func strPtr(s string) *string { return &s }

func TestLogQueryFiltersExcludeNamedAttr(t *testing.T) {
	q := testLogQuery()
	joined := func(attr string) (string, []any) {
		where, args := q.filters(&attr)
		return strings.Join(where, " AND "), args
	}

	sev, sevArgs := joined("Severity")
	assert.NotContains(t, sev, "SeverityNumber")
	assert.Contains(t, sev, "ServiceName")
	assert.Contains(t, sev, "LogAttributes")
	assertArgsContain(t, sevArgs, "e2e.facets")

	svc, svcArgs := joined("service.name")
	assert.Contains(t, svc, "SeverityNumber")
	assert.NotContains(t, svc, "ServiceName =")
	assertArgsContain(t, svcArgs, "e2e.facets")

	host, hostArgs := joined("host.name")
	assert.Contains(t, host, "SeverityNumber")
	assert.Contains(t, host, "ServiceName")
	assert.NotContains(t, host, "host.name")
	assertArgsContain(t, hostArgs, "e2e.facets")

	for _, a := range hostArgs {
		assert.NotContains(t, fmt.Sprint(a), "node-a")
	}
}

func TestFacetCountSQL(t *testing.T) {
	q, attr, ok := facetCountSQL("Severity")
	require.True(t, ok)
	require.NotNil(t, attr)
	assert.Equal(t, "Severity", *attr)
	assert.Contains(t, q, "multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1)")
	assert.Contains(t, q, "GROUP BY 1")
	assert.NotContains(t, q, "LIMIT")

	q, attr, ok = facetCountSQL("service.name")
	require.True(t, ok)
	assert.Equal(t, "service.name", *attr)
	assert.Contains(t, q, "SELECT ServiceName, count(1)")
	assert.Contains(t, q, "HAVING ServiceName != ''")
	assert.Contains(t, q, "LIMIT 1000")

	q, attr, ok = facetCountSQL("host.name")
	require.True(t, ok)
	assert.Equal(t, "host.name", *attr)
	assert.Contains(t, q, "if(LogAttributes[@attr] != '', LogAttributes[@attr], ResourceAttributes[@attr])")
	assert.Contains(t, q, "HAVING v != ''")
	assert.Contains(t, q, "LIMIT 1000")

	q, attr, ok = facetCountSQL("Cluster")
	require.True(t, ok)
	assert.Equal(t, "Cluster", *attr)
	assert.Contains(t, q, "SELECT count(1)")
	assert.NotContains(t, q, "GROUP BY")

	_, _, ok = facetCountSQL("k8s.pod.name")
	assert.False(t, ok)

	q, attr, ok = facetCountSQL("Source")
	require.True(t, ok)
	require.NotNil(t, attr)
	assert.Equal(t, "Source", *attr)
	assert.Contains(t, q, "startsWith(ServiceName, '/')")
	assert.Contains(t, q, "GROUP BY 1")
}

func TestFacetCountSQLNamespaceAndApplication(t *testing.T) {
	q, attr, ok := facetCountSQL("Namespace")
	require.True(t, ok)
	assert.Equal(t, "Namespace", *attr)
	assert.Contains(t, q, "startsWith(ServiceName, '/k8s')")
	assert.Contains(t, q, "k8s.namespace.name")
	assert.Contains(t, q, "'n/a'")
	assert.Contains(t, q, "GROUP BY 1")

	q, attr, ok = facetCountSQL("Application")
	require.True(t, ok)
	assert.Equal(t, "Application", *attr)
	assert.Contains(t, q, "startsWith(ServiceName, '/k8s')")
	assert.Contains(t, q, "LIMIT 1000")
}

func TestLogQueryFiltersNamespaceAndApplication(t *testing.T) {
	q := LogQuery{
		Ctx: timeseries.NewContext(1_700_000_000, 1_700_003_600, 15),
		Filters: []LogFilter{
			{Name: "Namespace", Op: "=", Value: "coroot-dev"},
			{Name: "Application", Op: "=", Value: "express-demo"},
			{Name: "Severity", Op: "=", Value: "error"},
		},
	}
	where, _ := q.filters(nil)
	joined := strings.Join(where, " AND ")
	assert.Contains(t, joined, logNamespaceExpr())
	assert.Contains(t, joined, logApplicationExpr())
	assert.Contains(t, joined, "SeverityNumber")

	where, _ = q.filters(strPtr("Namespace"))
	joined = strings.Join(where, " AND ")
	assert.NotContains(t, joined, logNamespaceExpr())
	assert.Contains(t, joined, logApplicationExpr())

	q.Filters = []LogFilter{{Name: "Namespace", Op: "!=", Value: "n/a"}}
	where, _ = q.filters(nil)
	assert.Contains(t, strings.Join(where, " AND "), "NOT (")
}

func TestLogQueryFiltersMessageContainsSubstring(t *testing.T) {
	q := LogQuery{
		Ctx: timeseries.NewContext(1_700_000_000, 1_700_003_600, 15),
		Filters: []LogFilter{
			{Name: "Message", Op: "contains", Value: "fail"},
		},
	}
	where, args := q.filters(nil)
	joined := strings.Join(where, " AND ")
	assert.Contains(t, joined, "positionCaseInsensitiveUTF8(Body,")
	assert.Contains(t, joined, "> 0")
	assert.NotContains(t, joined, "hasToken(")
	assertArgsContain(t, args, "fail")

	q.Filters = []LogFilter{{Name: "Message", Op: "contains", Value: "fail timeout"}}
	where, _ = q.filters(nil)
	joined = strings.Join(where, " AND ")
	assert.Equal(t, 2, strings.Count(joined, "positionCaseInsensitiveUTF8(Body,"))
	assert.Contains(t, joined, " AND ")

	q.Filters = []LogFilter{{Name: "Message", Op: "not contains", Value: "err"}}
	where, _ = q.filters(nil)
	joined = strings.Join(where, " AND ")
	assert.Contains(t, joined, "NOT (")
	assert.Contains(t, joined, "positionCaseInsensitiveUTF8(Body,")
	assert.NotContains(t, joined, "hasToken(")

	q.Filters = []LogFilter{{Name: "Message", Op: "contains", Value: "fail"}}
	where, _ = q.filters(strPtr("Cluster"))
	joined = strings.Join(where, " AND ")
	assert.Contains(t, joined, "positionCaseInsensitiveUTF8(Body,")
	where, _ = q.filters(strPtr("Severity"))
	joined = strings.Join(where, " AND ")
	assert.Contains(t, joined, "positionCaseInsensitiveUTF8(Body,")
}

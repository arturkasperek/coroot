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
}

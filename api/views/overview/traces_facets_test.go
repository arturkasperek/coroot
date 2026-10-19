package overview

import (
	"testing"

	"github.com/coroot/coroot/clickhouse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraceFacetGroupsFromMergedOrderAndSort(t *testing.T) {
	groups := traceFacetGroupsFromMerged(map[string]map[string]uint64{
		"SpanName": {
			"GET /api/error": 8,
			"GET /api/hello": 30,
		},
		"Server": {
			"coroot-dev/express-demo": 12,
			"coroot-dev/flask-demo":   40,
		},
		"ServiceName": {"express-demo": 12}, // not a facet of the Traces page
	})
	require.Len(t, groups, 2)
	assert.Equal(t, []string{"Server", "SpanName"}, []string{groups[0].Key, groups[1].Key})
	assert.Equal(t, []clickhouse.FacetValue{
		{Value: "coroot-dev/flask-demo", Count: 40},
		{Value: "coroot-dev/express-demo", Count: 12},
	}, groups[0].Values)
	assert.Equal(t, "GET /api/hello", groups[1].Values[0].Value)
}

func TestTraceFacetGroupsFromMergedIncludesNamespaceLastNA(t *testing.T) {
	groups := traceFacetGroupsFromMerged(map[string]map[string]uint64{
		"Namespace": {
			"n/a":         5,
			"coroot-dev":  40,
			"kube-system": 2,
		},
		"Server":   {"coroot-dev/express-demo": 12},
		"ApiRoute": {"GET /api/hello": 30},
		"SpanName": {"GET /api/hello": 30},
	})
	assert.Equal(t, []string{"Namespace", "Server", "ApiRoute", "SpanName"}, []string{groups[0].Key, groups[1].Key, groups[2].Key, groups[3].Key})
	assert.Equal(t, "coroot-dev", groups[0].Values[0].Value)
	assert.Equal(t, "n/a", groups[0].Values[len(groups[0].Values)-1].Value)
	assert.Equal(t, uint64(5), groups[0].Values[len(groups[0].Values)-1].Count)
}

func TestTraceFacetGroupsFromMergedPutsSourceFirst(t *testing.T) {
	groups := traceFacetGroupsFromMerged(map[string]map[string]uint64{
		"SpanName":  {"GET /api/hello": 30},
		"Source":    {"otel": 12, "agent": 40},
		"Namespace": {"coroot-dev": 52},
		"Server":    {"coroot-dev/express-demo": 12},
	})
	assert.Equal(t, []string{"Source", "Namespace", "Server", "SpanName"}, []string{groups[0].Key, groups[1].Key, groups[2].Key, groups[3].Key})
	assert.Equal(t, []clickhouse.FacetValue{{Value: "agent", Count: 40}, {Value: "otel", Count: 12}}, groups[0].Values)
}

func TestTraceFacetGroupsFromMergedOrdersStatusLikeTheChart(t *testing.T) {
	groups := traceFacetGroupsFromMerged(map[string]map[string]uint64{
		"Server": {"coroot-dev/flask-demo": 40},
		"Status": {"other": 900, "5xx": 3, "2xx": 50, "error": 1},
		"Source": {"otel": 10},
	})
	assert.Equal(t, []string{"Source", "Status", "Server"}, []string{groups[0].Key, groups[1].Key, groups[2].Key})
	assert.Equal(t, []clickhouse.FacetValue{
		{Value: "2xx", Count: 50},
		{Value: "5xx", Count: 3},
		{Value: "error", Count: 1},
		{Value: "other", Count: 900},
	}, groups[1].Values)
}

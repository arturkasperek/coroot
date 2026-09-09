package overview

import (
	"testing"

	"github.com/coroot/coroot/clickhouse"
	"github.com/stretchr/testify/assert"
)

func TestMergeFacetValuesSumsCounts(t *testing.T) {
	dst := map[string]uint64{"a": 10}
	mergeFacetValues(dst, []clickhouse.FacetValue{
		{Value: "a", Count: 5},
		{Value: "b", Count: 2},
		{Value: "", Count: 9},
	})
	assert.Equal(t, map[string]uint64{"a": 15, "b": 2}, dst)
}

func TestCompleteSeverityFacetsFillsZerosAndOrder(t *testing.T) {
	got := completeSeverityFacets([]clickhouse.FacetValue{
		{Value: "error", Count: 12},
		{Value: "info", Count: 38},
		{Value: "fatal", Count: 3},
	})
	assert.Equal(t, []clickhouse.FacetValue{
		{Value: "unknown", Count: 0},
		{Value: "info", Count: 38},
		{Value: "warning", Count: 0},
		{Value: "error", Count: 12},
	}, got)
}

func TestFacetGroupsFromMergedEmptyIsNonNil(t *testing.T) {
	groups := facetGroupsFromMerged(map[string]map[string]uint64{})
	assert.NotNil(t, groups)
	assert.Len(t, groups, 0)
}

func TestFacetGroupsFromMergedCompletesSource(t *testing.T) {
	groups := facetGroupsFromMerged(map[string]map[string]uint64{
		"Source": {"agent": 40},
	})
	assert.Equal(t, []clickhouse.FacetValue{
		{Value: "agent", Count: 40},
		{Value: "otel", Count: 0},
	}, groups[0].Values)
	assert.Equal(t, "Source", groups[0].Key)
}

func TestFacetGroupsFromMergedKeepsClusterRowsAndSorts(t *testing.T) {
	merged := map[string]map[string]uint64{
		"Source": {"agent": 40, "otel": 10},
		"Namespace": {
			"coroot-dev": 40,
			"n/a":        10,
		},
		"Application": {
			"nextjs-demo":  10,
			"express-demo": 40,
		},
		"Cluster": {
			"default": 50,
			"other":   7,
		},
		"Severity": {"info": 38, "error": 12},
	}
	groups := facetGroupsFromMerged(merged)
	byKey := map[string]clickhouse.FacetGroup{}
	for _, g := range groups {
		byKey[g.Key] = g
	}
	assert.Equal(t, []clickhouse.FacetValue{
		{Value: "unknown", Count: 0},
		{Value: "info", Count: 38},
		{Value: "warning", Count: 0},
		{Value: "error", Count: 12},
	}, byKey["Severity"].Values)
	assert.Equal(t, "coroot-dev", byKey["Namespace"].Values[0].Value)
	assert.Equal(t, uint64(40), byKey["Namespace"].Values[0].Count)
	assert.Equal(t, "n/a", byKey["Namespace"].Values[1].Value)
	assert.Equal(t, "express-demo", byKey["Application"].Values[0].Value)
	assert.Equal(t, uint64(40), byKey["Application"].Values[0].Count)
	assert.Len(t, byKey["Cluster"].Values, 2)
	assert.Equal(t, []clickhouse.FacetValue{
		{Value: "agent", Count: 40},
		{Value: "otel", Count: 10},
	}, byKey["Source"].Values)
}

func TestFacetGroupsFromMergedNamespaceAndApplication(t *testing.T) {
	groups := facetGroupsFromMerged(map[string]map[string]uint64{
		"Namespace":   {"coroot-dev": 32},
		"Application": {"express-demo": 23, "nextjs-demo": 7},
		"Severity":    {"info": 10},
		"Source":      {"agent": 20},
	})
	keys := make([]string, len(groups))
	for i, g := range groups {
		keys[i] = g.Key
	}
	assert.Equal(t, []string{"Source", "Severity", "Namespace", "Application"}, keys)

	by := map[string]clickhouse.FacetGroup{}
	for _, g := range groups {
		by[g.Key] = g
	}
	assert.Equal(t, uint64(32), by["Namespace"].Values[0].Count)
	var hasNA bool
	for _, v := range by["Namespace"].Values {
		if v.Value == "n/a" {
			hasNA = true
			assert.Equal(t, uint64(0), v.Count)
		}
	}
	assert.True(t, hasNA)
	assert.Equal(t, "express-demo", by["Application"].Values[0].Value)
}

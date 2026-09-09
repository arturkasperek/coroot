package overview

import (
	"testing"

	"github.com/coroot/coroot/clickhouse"
	"github.com/stretchr/testify/assert"
)

func TestTraceFacetGroupsFromMergedOrderAndSort(t *testing.T) {
	groups := traceFacetGroupsFromMerged(map[string]map[string]uint64{
		"SpanName": {
			"GET /api/error": 8,
			"GET /api/hello": 30,
		},
		"ServiceName": {
			"express-demo": 12,
			"flask-demo":   40,
		},
	})
	assert.Equal(t, []string{"ServiceName", "SpanName"}, []string{groups[0].Key, groups[1].Key})
	assert.Equal(t, []clickhouse.FacetValue{
		{Value: "flask-demo", Count: 40},
		{Value: "express-demo", Count: 12},
	}, groups[0].Values)
	assert.Equal(t, "GET /api/hello", groups[1].Values[0].Value)
}

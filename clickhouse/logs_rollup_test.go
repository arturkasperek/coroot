package clickhouse

import (
	"strings"
	"testing"

	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

// 2023-11-14 22:13:20 UTC is minute-aligned: 1_699_999_980 = 28333333 * 60
const (
	alignedFrom = 1_699_999_980
	alignedTo   = alignedFrom + 3600
)

func rollupQuery(step timeseries.Duration, filters ...LogFilter) LogQuery {
	return LogQuery{Ctx: timeseries.NewContext(alignedFrom, alignedTo, step), Filters: filters}
}

func TestLogRollupEligibility(t *testing.T) {
	cases := []struct {
		name string
		q    LogQuery
		ok   bool
	}{
		{"no filters", rollupQuery(60), true},
		{"severity, service, namespace, application, source", rollupQuery(60,
			LogFilter{Name: "Severity", Op: "=", Value: "error"},
			LogFilter{Name: "service.name", Op: "~", Value: "^/k8s/"},
			LogFilter{Name: "Namespace", Op: "!=", Value: "n/a"},
			LogFilter{Name: "Application", Op: "=", Value: "api"},
			LogFilter{Name: "Source", Op: "=", Value: "agent"}), true},
		{"host.name", rollupQuery(60, LogFilter{Name: "host.name", Op: "=", Value: "node-a"}), true},
		{"message text needs the raw rows", rollupQuery(60, LogFilter{Name: "Message", Op: "contains", Value: "timeout"}), false},
		{"a message filter with nothing to search for changes nothing", rollupQuery(60, LogFilter{Name: "Message", Op: "contains", Value: "** --"}), true},
		{"trace id", rollupQuery(60, LogFilter{Name: "TraceId", Op: "=", Value: "abc"}), false},
		{"another attribute", rollupQuery(60, LogFilter{Name: "k8s.pod.name", Op: "=", Value: "p"}), false},
		{"live tail", func() LogQuery { q := rollupQuery(60); q.Since = q.Ctx.From.ToStandard(); return q }(), false},
		{"window starting inside a minute", LogQuery{Ctx: timeseries.NewContext(alignedFrom+15, alignedTo, 15)}, false},
		{"window ending inside a minute", LogQuery{Ctx: timeseries.NewContext(alignedFrom, alignedTo+15, 15)}, false},
	}
	for _, c := range cases {
		_, _, ok := c.q.rollupFilters(nil)
		assert.Equal(t, c.ok, ok, c.name)
	}
	// ignoring the filter a facet counts for can make a query eligible
	q := rollupQuery(60, LogFilter{Name: "k8s.pod.name", Op: "=", Value: "p"})
	_, _, ok := q.rollupFilters(strPtr("k8s.pod.name"))
	assert.True(t, ok)
}

func TestLogRollupFilters(t *testing.T) {
	q := rollupQuery(60,
		LogFilter{Name: "host.name", Op: "=", Value: "node-a"},
		LogFilter{Name: "Severity", Op: "=", Value: "error"},
	)
	where, args, ok := q.rollupFilters(nil)
	assert.True(t, ok)
	joined := strings.Join(where, " AND ")
	assert.Contains(t, joined, "Minute >= toDateTime(@from) AND Minute < toDateTime(@to)")
	assert.Contains(t, joined, "(HostLog = @attr_values_")
	assert.Contains(t, joined, "SeverityNumber BETWEEN")
	assert.NotContains(t, joined, "Timestamp")
	assert.NotContains(t, joined, "LogAttributes")
	assert.NotEmpty(t, args)
}

func TestLogRollupFacetSQL(t *testing.T) {
	for _, name := range []string{"Severity", "service.name", "host.name", "Cluster", "Source", "Namespace", "Application"} {
		raw, _, ok := facetCountSQL(name)
		assert.True(t, ok, name)
		rollup, ok := rollupFacetCountSQL(name)
		assert.True(t, ok, name)
		assert.Contains(t, rollup, "@@table_otel_logs_rollup@@", name)
		assert.Contains(t, rollup, "sum(Count)", name)
		assert.NotContains(t, rollup, "count(1)", name)
		// the same shape, so the Go side scans both the same way
		assert.Equal(t, strings.Count(raw, "GROUP BY"), strings.Count(rollup, "GROUP BY"), name)
	}
	_, ok := rollupFacetCountSQL("nope")
	assert.False(t, ok)
}

// The e2e storage tests (e2e/storage) take their "raw table" numbers from a
// query that carries a filter on an attribute the rollup does not keep. This
// pins that such a filter rules the rollup out and still matches every row.
func TestRollupExcludedByUnknownAttributeFilter(t *testing.T) {
	q := rollupQuery(300, LogFilter{Name: "no.such.attribute", Op: "!=", Value: "zzz"})
	_, _, ok := q.rollupFilters(nil)
	assert.False(t, ok)
	where, _ := q.filters(nil)
	assert.Contains(t, strings.Join(where, " "), "LogAttributes[@attr_name_0_0] != @attr_values_0_0")
}

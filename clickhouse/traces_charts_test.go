package clickhouse

import (
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

// The Traces page counts its charts, facets and list over one window. A short
// view gets steps of seconds (read from otel_traces), so that its last 15
// seconds are its last 15 seconds; a long one the step of the view rounded up
// to whole minutes (read from the per-minute histogram table).
func TestTracesWindowStep(t *testing.T) {
	for _, c := range []struct {
		duration timeseries.Duration
		viewStep timeseries.Duration
		want     timeseries.Duration
	}{
		{15 * timeseries.Second, 15, 1},
		{5 * timeseries.Minute, 15, 5},
		{15 * timeseries.Minute, 15, 15},
		{timeseries.Hour, 15, 60},
		{6 * timeseries.Hour, 60, 60},
		{7 * 24 * timeseries.Hour, 15 * timeseries.Minute, 15 * timeseries.Minute},
		{2 * timeseries.Hour, 90, 120},
	} {
		w := NewTracesWindow(1_000_000, timeseries.Time(1_000_000).Add(c.duration), false, c.viewStep)
		assert.Equal(t, c.want, w.Step, "%v", c.duration)
	}
}

// A live view (up to now) ends at now: its last bucket is the one in progress.
// A view in the past ends at the end of the bucket of its end, so that every
// bucket is whole.
func TestTracesWindowRange(t *testing.T) {
	live := NewTracesWindow(1_000_010, 1_003_625, true, 15)
	assert.Equal(t, timeseries.Duration(60), live.Step)
	assert.Equal(t, timeseries.Time(999_960), live.From, "aligned to the step")
	assert.Equal(t, timeseries.Time(1_003_625), live.To, "now")
	assert.Equal(t, 62, live.Points())
	assert.Equal(t, timeseries.Context{From: 999_960, To: 1_003_620, Step: 60}, live.ChartContext())

	past := NewTracesWindow(1_000_010, 1_003_625, false, 15)
	assert.Equal(t, timeseries.Time(999_960), past.From)
	assert.Equal(t, timeseries.Time(1_003_680), past.To, "the end of its last bucket")
	assert.Equal(t, 62, past.Points())

	aligned := NewTracesWindow(1_000_020, 1_000_035, true, 15)
	assert.Equal(t, timeseries.Duration(1), aligned.Step)
	assert.Equal(t, timeseries.Time(1_000_020), aligned.From, "the last 15 seconds")
	assert.Equal(t, 15, aligned.Points())
}

func TestStatusFilterUsesTheColumnOfTheHistogramTable(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("Status", "=", "5xx")
	assert.True(t, q.filtersOnHistogramDimensions())

	filter, args := q.Filter(true)
	assert.Equal(t, []string{"(StatusClass) = @filter_0"}, filter)
	assert.Equal(t, []any{clickhouse.Named("filter_0", "5xx")}, args)

	filter, _ = q.Filter(false)
	assert.Equal(t, []string{"(" + ch.TraceStatusClassExpression + ") = @filter_0"}, filter)
}

func TestApplicationFiltersUseTheHistogramTable(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("Server", "=", "project:coroot-dev:Deployment:express-demo")
	q.AddFilter("Client", "!=", "project:coroot-dev:Deployment:demo-traffic")
	assert.True(t, q.filtersOnHistogramDimensions())

	filter, _ := q.Filter(true)
	assert.Equal(t, []string{"Server = @filter_0", "Client != @filter_1"}, filter)
}

// Namespace is the part of Server before the slash; = and != compare the
// prefix, so that ClickHouse reads only the namespace through the sorting key.
func TestNamespaceFilterUsesThePrefixOfServer(t *testing.T) {
	q := SpanQuery{}
	q.AddFilter("Namespace", "=", "coroot-dev")
	q.AddFilter("Namespace", "=", "n/a") // an application with no namespace
	q.AddFilter("Namespace", "!=", "kube-system")
	q.AddFilter("Namespace", "~", "^coroot")
	assert.True(t, q.filtersOnHistogramDimensions())

	filter, args := q.Filter(true)
	assert.Equal(t, []string{
		"NOT startsWith(Server, concat(@filter_2, '/'))",
		"(startsWith(Server, concat(@filter_0, '/')) OR position(Server, '/') = 0 OR match(" + traceNamespaceExpr + ", @filter_3))",
	}, filter)
	assert.Len(t, args, 4)
}

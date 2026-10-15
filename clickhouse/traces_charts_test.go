package clickhouse

import (
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

func TestTracesChartContextAlignsToWholeMinutes(t *testing.T) {
	viewContext := timeseries.Context{From: 1_000_015, To: 1_003_615, Step: 15}
	chartContext := TracesChartContext(viewContext)
	assert.Equal(t, timeseries.Duration(60), chartContext.Step)
	assert.Equal(t, timeseries.Time(999_960), chartContext.From)
	assert.Equal(t, timeseries.Time(1_003_560), chartContext.To)

	viewContext = timeseries.Context{From: 1_200_000, To: 1_290_000, Step: 120}
	assert.Equal(t, viewContext, TracesChartContext(viewContext))
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

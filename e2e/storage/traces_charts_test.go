//go:build e2e

package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One root span per case: the status class its attributes and status give.
var statusClassSpans = []struct {
	name         string
	attributes   string
	statusCode   string
	milliseconds int
	statusClass  string
}{
	{"ok", "map('http.status_code', '200')", "STATUS_CODE_UNSET", 120, "2xx"},
	{"not modified, new convention", "map('http.response.status_code', '304')", "STATUS_CODE_UNSET", 3, "3xx"},
	{"not found", "map('http.status_code', '404')", "STATUS_CODE_UNSET", 7, "4xx"},
	{"unavailable", "map('http.status_code', '503')", "STATUS_CODE_ERROR", 600, "5xx"},
	{"failed query", "map()", "STATUS_CODE_ERROR", 30, "error"},
	{"internal", "map()", "STATUS_CODE_UNSET", 2, "other"},
	{"not a number", "map('http.status_code', 'abc')", "STATUS_CODE_UNSET", 2, "other"},
	{"out of range, failed", "map('http.status_code', '600')", "STATUS_CODE_ERROR", 2, "error"},
	{"informational", "map('http.status_code', '101')", "STATUS_CODE_UNSET", 2, "other"},
}

func insertStatusClassSpans(t *testing.T, e *chtest.Env, timestamp time.Time) {
	t.Helper()
	var values []string
	for index, span := range statusClassSpans {
		values = append(values, fmt.Sprintf("(toDateTime64(%d, 9), 'trace-%d', 'span-%d', '', '%s', 'SPAN_KIND_SERVER', 'service', %s, %d, '%s')",
			timestamp.Unix(), index, index, span.name, span.attributes, span.milliseconds*1_000_000, span.statusCode))
	}
	e.Exec(t, "INSERT INTO otel_traces (Timestamp, TraceId, SpanId, ParentSpanId, SpanName, SpanKind, ServiceName, SpanAttributes, Duration, StatusCode) VALUES "+strings.Join(values, ", "))
}

func sumOf(series *timeseries.TimeSeries) float32 {
	if series == nil {
		return 0
	}
	return series.Reduce(timeseries.NanSum)
}

func TestTraceStatusClasses(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	time.Sleep(time.Second) // the view's creation time has a second precision
	now := time.Now()
	spansAt := now.Add(10 * time.Minute)
	insertStatusClassSpans(t, e, spansAt)

	client, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer client.Close()

	t.Run("the expression", func(t *testing.T) {
		rows, err := client.Query(ctx, "SELECT SpanName, "+ch.TraceStatusClassExpression+" FROM @@table_otel_traces@@")
		require.NoError(t, err)
		defer rows.Close()
		classes := map[string]string{}
		for rows.Next() {
			var name, class string
			require.NoError(t, rows.Scan(&name, &class))
			classes[name] = class
		}
		for _, span := range statusClassSpans {
			assert.Equal(t, span.statusClass, classes[span.name], span.name)
		}
	})

	viewContext := timeseries.NewContext(timeseries.Time(now.Add(5*time.Second).Unix()), timeseries.Time(now.Add(time.Hour).Unix()), 60)
	expected := map[string]float32{"2xx": 1, "3xx": 1, "4xx": 1, "5xx": 1, "error": 2, "other": 3}
	for _, fromMV := range []bool{true, false} {
		query := clickhouse.SpanQuery{Ctx: viewContext}
		if !fromMV {
			query.AddFilter("Namespace", "!=", "none") // a filter the histogram table cannot serve
		}
		t.Run(fmt.Sprintf("the chart counts, from the histogram table: %v", fromMV), func(t *testing.T) {
			counts, err := client.GetRootSpansCounts(ctx, query)
			require.NoError(t, err)
			actual := map[string]float32{}
			for class, series := range counts.ByStatusClass {
				actual[class] = sumOf(series)
			}
			assert.Equal(t, expected, actual)
			var requests float32
			for _, series := range counts.ByDurationBucket {
				requests += sumOf(series)
			}
			assert.Equal(t, float32(len(statusClassSpans)), requests)
		})
		t.Run(fmt.Sprintf("a status filter narrows the charts, from the histogram table: %v", fromMV), func(t *testing.T) {
			query := query
			query.Filters = append(append([]clickhouse.SpanFilter{}, query.Filters...), clickhouse.SpanFilter{Field: "Status", Op: "=", Value: "5xx"})
			counts, err := client.GetRootSpansCounts(ctx, query)
			require.NoError(t, err)
			require.Len(t, counts.ByStatusClass, 1)
			assert.Equal(t, float32(1), sumOf(counts.ByStatusClass["5xx"]))
			require.Len(t, counts.ByDurationBucket, 1)
			assert.Equal(t, float32(1), sumOf(counts.ByDurationBucket[500]))
		})
	}

	t.Run("a status filter narrows the trace list", func(t *testing.T) {
		query := clickhouse.SpanQuery{Ctx: viewContext, TsFrom: viewContext.From, TsTo: viewContext.To, Limit: 100}
		query.AddFilter("Status", "=", "error")
		spans, err := client.GetRootSpans(ctx, query)
		require.NoError(t, err)
		var names []string
		for _, span := range spans {
			names = append(names, span.Name)
		}
		assert.ElementsMatch(t, []string{"failed query", "out of range, failed"}, names)
	})
}

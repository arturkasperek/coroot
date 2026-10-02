//go:build e2e

package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/collector"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

func attr(k, v string) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}}}
}

func traceID(n byte) []byte { b := make([]byte, 16); b[15] = n; return b }
func spanID(n byte) []byte  { b := make([]byte, 8); b[7] = n; return b }

// Spans go in through the real collector write path, and come out through the
// real query methods.
func TestTraceBodiesRoundTrip(t *testing.T) {
	e := chtest.New(t)
	ll := e.LL
	ctx := context.Background()
	start := time.Now().Add(-time.Minute)
	ns := func(d time.Duration) uint64 { return uint64(start.Add(d).UnixNano()) }

	huge := strings.Repeat("x", 40000)
	batch := collector.NewTracesBatch(100000, time.Hour, func(q ch.Query) error { return ll.Do(ctx, q) })
	batch.Add(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{attr("service.name", "orders")}},
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{
			{
				TraceId: traceID(1), SpanId: spanID(1), Name: "POST /orders", Kind: tracev1.Span_SPAN_KIND_SERVER,
				StartTimeUnixNano: ns(0), EndTimeUnixNano: ns(50 * time.Millisecond),
				Attributes: []*commonv1.KeyValue{
					attr("http.method", "POST"), attr("http.status_code", "201"),
					attr("http.request.body", `{"order":"ord-715688382"}`),
					attr("http.response.body", `{"id":"abc"}`),
				},
			},
			{
				TraceId: traceID(1), SpanId: spanID(2), ParentSpanId: spanID(1), Name: "GET /stock", Kind: tracev1.Span_SPAN_KIND_CLIENT,
				StartTimeUnixNano: ns(5 * time.Millisecond), EndTimeUnixNano: ns(20 * time.Millisecond),
				Attributes: []*commonv1.KeyValue{attr("http.method", "GET")},
			},
			{
				TraceId: traceID(2), SpanId: spanID(3), Name: "POST /big", Kind: tracev1.Span_SPAN_KIND_SERVER,
				StartTimeUnixNano: ns(time.Second), EndTimeUnixNano: ns(time.Second + time.Millisecond),
				Attributes: []*commonv1.KeyValue{attr("http.request.body", huge)},
			},
		}}},
	}}})
	batch.Close()

	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()
	traceHex := "00000000000000000000000000000001"

	t.Run("the single-trace view carries the bodies", func(t *testing.T) {
		spans, err := c.GetSpansByTraceId(ctx, traceHex, true)
		require.NoError(t, err)
		require.Len(t, spans, 2)
		root := spans[0]
		assert.Equal(t, `{"order":"ord-715688382"}`, root.RequestBody)
		assert.Equal(t, `{"id":"abc"}`, root.ResponseBody)
		assert.Empty(t, spans[1].RequestBody)
	})
	t.Run("without the flag nothing reads or returns them", func(t *testing.T) {
		spans, err := c.GetSpansByTraceId(ctx, traceHex, false)
		require.NoError(t, err)
		require.Len(t, spans, 2)
		for _, s := range spans {
			assert.Empty(t, s.RequestBody)
			assert.Empty(t, s.ResponseBody)
		}
	})
	t.Run("lists do not carry them", func(t *testing.T) {
		q := clickhouse.SpanQuery{
			Ctx:    timeseries.NewContext(timeseries.Time(start.Add(-time.Hour).Unix()), timeseries.Time(start.Add(time.Hour).Unix()), 15),
			TsFrom: timeseries.Time(start.Add(-time.Hour).Unix()), TsTo: timeseries.Time(start.Add(time.Hour).Unix()),
			Limit: 100,
		}
		spans, err := c.GetRootSpans(ctx, q)
		require.NoError(t, err)
		require.NotEmpty(t, spans)
		for _, s := range spans {
			assert.Empty(t, s.RequestBody)
		}
	})
	t.Run("bodies never stay in SpanAttributes", func(t *testing.T) {
		var leaked uint64
		require.NoError(t, c.QueryRow(ctx, "SELECT countIf(mapContains(SpanAttributes, 'http.request.body') OR mapContains(SpanAttributes, 'http.response.body')) FROM @@table_otel_traces@@").Scan(&leaked))
		assert.Zero(t, leaked)
	})
	t.Run("a body over the cap is cut", func(t *testing.T) {
		var n uint64
		require.NoError(t, c.QueryRow(ctx, "SELECT max(length(RequestBody)) FROM @@table_otel_traces@@").Scan(&n))
		assert.Equal(t, uint64(16*1024), n)
	})
	t.Run("a body is found by one of its words", func(t *testing.T) {
		var n uint64
		require.NoError(t, c.QueryRow(ctx, "SELECT count() FROM @@table_otel_traces@@ WHERE hasToken(RequestBody, '715688382')").Scan(&n))
		assert.Equal(t, uint64(1), n)
	})
}

// The point of the index: a search for a rare word skips almost every granule.
func TestTraceBodyIndexSkipsGranules(t *testing.T) {
	e := chtest.New(t)
	e.Exec(t, fmt.Sprintf(`
INSERT INTO otel_traces (Timestamp, TraceId, SpanId, ParentSpanId, TraceState, SpanName, SpanKind, ServiceName, ResourceAttributes, SpanAttributes, Duration, StatusCode, StatusMessage, RequestBody, ResponseBody)
SELECT now() - number, hex(number), hex(number), '', '', 'POST /x', 'SPAN_KIND_SERVER', 'svc', map(), map(), 1, '', '',
  concat('{"order":"ord-', toString(number), '","note":"', repeat('lorem ipsum ', 20), '"}'), '{}'
FROM numbers(%d)`, 300000))
	e.Exec(t, "OPTIMIZE TABLE otel_traces FINAL")
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()

	rows, err := c.Query(context.Background(), "EXPLAIN indexes = 1 SELECT count() FROM @@table_otel_traces@@ WHERE hasToken(RequestBody, 'ord') AND hasToken(RequestBody, '271828')")
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var l string
		require.NoError(t, rows.Scan(&l))
		plan = append(plan, l)
	}
	text := strings.Join(plan, "\n")
	require.Contains(t, text, "idx_request_body", text)
	var kept, total int
	for i, l := range plan {
		if strings.Contains(l, "idx_request_body") {
			for _, l2 := range plan[i:] {
				if _, err := fmt.Sscanf(strings.TrimSpace(l2), "Granules: %d/%d", &kept, &total); err == nil {
					break
				}
			}
			break
		}
	}
	require.Greater(t, total, 20, text)
	assert.LessOrEqual(t, kept, total/10, text)
}

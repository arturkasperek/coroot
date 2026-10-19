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
	"github.com/coroot/coroot/model"
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
	batch := collector.NewTracesBatch(100000, time.Hour, func(q ch.Query) error { return ll.Do(ctx, q) }, nil, nil)
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
		require.NoError(t, c.QueryRow(ctx, "SELECT max(length(Body)) FROM @@table_trace_bodies@@").Scan(&n))
		assert.Equal(t, uint64(16*1024), n)
	})
	t.Run("a body is found by one of its words, and its spans by its hash", func(t *testing.T) {
		var n uint64
		require.NoError(t, c.QueryRow(ctx, `
SELECT count() FROM @@table_otel_traces@@
WHERE RequestBodyHash GLOBAL IN (SELECT Hash FROM @@table_trace_bodies@@ WHERE hasToken(Body, '715688382'))`).Scan(&n))
		assert.Equal(t, uint64(1), n)
	})
}

// The point of the index: a search for a rare word skips almost every granule.
func TestTraceBodyIndexSkipsGranules(t *testing.T) {
	e := chtest.New(t)
	e.Exec(t, fmt.Sprintf(`
INSERT INTO trace_bodies (Hash, Body, LastSeen)
SELECT number + 1, concat('{"order":"ord-', toString(number), '","note":"', repeat('lorem ipsum ', 20), '"}'), now()
FROM numbers(%d)`, 300000))
	e.Exec(t, "OPTIMIZE TABLE trace_bodies FINAL")
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()

	rows, err := c.Query(context.Background(), "EXPLAIN indexes = 1 SELECT max(length(Body)) FROM trace_bodies WHERE hasToken(Body, 'ord') AND hasToken(Body, '271828')")
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var l string
		require.NoError(t, rows.Scan(&l))
		plan = append(plan, l)
	}
	text := strings.Join(plan, "\n")
	require.Contains(t, text, "idx_body", text)
	var kept, total int
	for i, l := range plan {
		if strings.TrimSpace(l) == "Name: idx_body" { // the skip index section, not the prewhere column
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

// The two sides of a request and repeated answers carry the same body: it is
// stored once, and every span that carries it reads it.
func TestTraceBodyStoredOnce(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	start := time.Now().Add(-time.Minute)
	ns := func(d time.Duration) uint64 { return uint64(start.Add(d).UnixNano()) }
	body := `{"answer":"the same for everyone"}`
	var spans []*tracev1.Span
	for n := byte(1); n <= 5; n++ {
		spans = append(spans, &tracev1.Span{
			TraceId: traceID(n), SpanId: spanID(n), Name: "GET /answer", Kind: tracev1.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: ns(time.Duration(n) * time.Millisecond), EndTimeUnixNano: ns(time.Duration(n)*time.Millisecond + time.Millisecond),
			Attributes: []*commonv1.KeyValue{attr("http.method", "GET"), attr("http.response.body", body)},
		})
	}
	// two batches: the second must not store it again either
	for _, part := range [][]*tracev1.Span{spans[:2], spans[2:]} {
		batch := collector.NewTracesBatch(100000, time.Hour, func(q ch.Query) error { return e.LL.Do(ctx, q) }, nil, nil)
		batch.Add(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
			Resource:   &resourcev1.Resource{Attributes: []*commonv1.KeyValue{attr("service.name", "answers")}},
			ScopeSpans: []*tracev1.ScopeSpans{{Spans: part}},
		}}})
		batch.Close()
	}
	e.Exec(t, "OPTIMIZE TABLE trace_bodies FINAL")

	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()
	var rows uint64
	require.NoError(t, c.QueryRow(ctx, "SELECT count() FROM @@table_trace_bodies@@").Scan(&rows))
	assert.Equal(t, uint64(1), rows, "one row for the body of five spans")

	for n := byte(1); n <= 5; n++ {
		trace, err := c.GetSpansByTraceId(ctx, fmt.Sprintf("%032x", n), true)
		require.NoError(t, err)
		require.Len(t, trace, 1)
		assert.Equal(t, body, trace[0].ResponseBody)
		assert.Empty(t, trace[0].RequestBody)
	}
}

// The server skips the bodies its client captured: the details of a server
// span with no body show the bodies of the client span of the same request,
// found by RequestKey within a few seconds, and only when its method and
// target match — no body rather than another request's.
func TestServerSpanShowsTheBodiesOfItsClientSpan(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	now := time.Now().Add(-time.Minute).UTC()
	at := func(d time.Duration) string { return now.Add(d).Format("2006-01-02 15:04:05.000000000") }
	e.Exec(t, `INSERT INTO trace_bodies (Hash, Body, LastSeen) VALUES (11, '{"order":1}', now()), (12, '{"id":"ab"}', now()), (13, '{"other":true}', now())`)
	e.Exec(t, fmt.Sprintf(`
INSERT INTO otel_traces (Timestamp, TraceId, SpanId, SpanName, SpanKind, ServiceName, SpanAttributes, Duration, RequestKey, RequestBodyHash, ResponseBodyHash) VALUES
  -- the client span of a request, with its bodies
  ('%s', 'c1', 'c1', 'POST', 'SPAN_KIND_CLIENT', '/k8s/shop/checkout', map('http.method', 'POST', 'http.target', '/orders'), 5000000, 100, 11, 12),
  -- its server span: the same key, no bodies
  ('%s', 's1', 's1', 'POST', 'SPAN_KIND_SERVER', '/k8s/shop/orders', map('http.method', 'POST', 'http.target', '/orders'), 3000000, 100, 0, 0),
  -- a server span whose key matches a client span of another request (numbers out of step)
  ('%s', 'c2', 'c2', 'GET', 'SPAN_KIND_CLIENT', '/k8s/shop/checkout', map('http.method', 'GET', 'http.target', '/other'), 5000000, 200, 13, 0),
  ('%s', 's2', 's2', 'POST', 'SPAN_KIND_SERVER', '/k8s/shop/orders', map('http.method', 'POST', 'http.target', '/orders'), 3000000, 200, 0, 0),
  -- a server span whose client span is a minute away (a 4-tuple used again)
  ('%s', 'c3', 'c3', 'POST', 'SPAN_KIND_CLIENT', '/k8s/shop/checkout', map('http.method', 'POST', 'http.target', '/orders'), 5000000, 300, 11, 12),
  ('%s', 's3', 's3', 'POST', 'SPAN_KIND_SERVER', '/k8s/shop/orders', map('http.method', 'POST', 'http.target', '/orders'), 3000000, 300, 0, 0)`,
		at(0), at(time.Millisecond), at(time.Second), at(time.Second+time.Millisecond), at(-time.Minute), at(2*time.Second)))
	e.Exec(t, `INSERT INTO otel_traces_trace_id_ts (TraceId, Start, End) SELECT TraceId, min(Timestamp), max(Timestamp) FROM otel_traces GROUP BY TraceId`)

	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()
	spanOf := func(traceId string) *model.TraceSpan {
		spans, err := c.GetSpansByTraceId(ctx, traceId, true)
		require.NoError(t, err)
		require.Len(t, spans, 1)
		return spans[0]
	}

	server := spanOf("s1")
	assert.Equal(t, `{"order":1}`, server.RequestBody, "the client span's bodies")
	assert.Equal(t, `{"id":"ab"}`, server.ResponseBody)

	other := spanOf("s2")
	assert.Empty(t, other.RequestBody, "the client span of the key is another request")
	assert.Empty(t, other.ResponseBody)

	late := spanOf("s3")
	assert.Empty(t, late.RequestBody, "a client span outside the window is another connection")

	client := spanOf("c1")
	assert.Equal(t, `{"order":1}`, client.RequestBody, "a span with its own bodies keeps them")
}

// A restarted collector (a new batch) knows from trace_server_sources that the
// server records its side of HTTP: the client span of a request to it, though
// no server span reached this batch, is not counted.
func TestServerSpanSourcesSurviveARestart(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	start := time.Now().Add(-time.Minute)
	ns := func(d time.Duration) uint64 { return uint64(start.Add(d).UnixNano()) }
	// the rows are read back at once: the insert returns when the shard has them
	exec := func(q ch.Query) error { q.Settings = append(q.Settings, foreground...); return e.LL.Do(ctx, q) }
	spanOf := func(service string, n byte, kind tracev1.Span_SpanKind, attributes ...*commonv1.KeyValue) *v1.ExportTraceServiceRequest {
		return &v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
			Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{attr("service.name", service)}},
			ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
				TraceId: traceID(n), SpanId: spanID(n), Name: "GET /pay", Kind: kind,
				StartTimeUnixNano: ns(time.Duration(n) * time.Millisecond), EndTimeUnixNano: ns(time.Duration(n)*time.Millisecond + time.Millisecond),
				Attributes: append(attributes, attr("http.method", "GET")),
			}}}},
		}}}
	}

	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()
	reader := func() (*clickhouse.Client, error) { return c, nil }

	// before the restart: the SDK of payments records the server side
	before := collector.NewTracesBatch(100000, time.Hour, exec, reader, nil)
	before.Add(spanOf("payments", 1, tracev1.Span_SPAN_KIND_SERVER))
	before.Close()

	// after it: the first span is the client side of a request to payments
	after := collector.NewTracesBatch(100000, time.Hour, exec, reader, nil)
	after.Add(spanOf("checkout", 2, tracev1.Span_SPAN_KIND_CLIENT, attr("server.address", "payments")))
	after.Close()

	var server, protocol string
	var counted uint8
	require.NoError(t, c.QueryRow(ctx, "SELECT Server, Protocol, Counted FROM @@table_otel_traces@@ WHERE TraceId = '"+fmt.Sprintf("%032x", 2)+"'").Scan(&server, &protocol, &counted))
	assert.Equal(t, "payments", server)
	assert.Equal(t, "http", protocol)
	assert.Equal(t, uint8(0), counted, "the server's span counts the request")
}

// Two Coroot replicas: the agent of catalog, a pod started a moment ago, sends
// its server span to one, the agent of basket its client span of a request to
// catalog to the other. The other learns catalog's address from
// trace_addresses, which it reads every few seconds, and names it as the
// server.
func TestRecentAddressesAreSharedByReplicas(t *testing.T) {
	e := chtest.New(t)
	ctx := context.Background()
	exec := func(q ch.Query) error { q.Settings = append(q.Settings, foreground...); return e.LL.Do(ctx, q) }
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()
	reader := func() (*clickhouse.Client, error) { return c, nil }
	agentSpan := func(service string, n byte, kind tracev1.Span_SpanKind, attributes ...*commonv1.KeyValue) *v1.ExportTraceServiceRequest {
		now := uint64(time.Now().UnixNano())
		return &v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
			Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{attr("service.name", service)}},
			ScopeSpans: []*tracev1.ScopeSpans{{
				Scope: &commonv1.InstrumentationScope{Name: "coroot-node-agent"},
				Spans: []*tracev1.Span{{
					TraceId: traceID(n), SpanId: spanID(n), Name: "GET", Kind: kind, StartTimeUnixNano: now, EndTimeUnixNano: now + 1000,
					Attributes: append(attributes, attr("http.method", "GET")),
				}},
			}},
		}}}
	}

	other := collector.NewTracesBatch(100000, time.Hour, exec, reader, nil) // running already
	one := collector.NewTracesBatch(100000, time.Hour, exec, reader, nil)
	one.Add(agentSpan("/k8s/shop/catalog", 1, tracev1.Span_SPAN_KIND_SERVER, attr("server.address", "10.42.0.200"), attr("server.port", "8080")))
	one.Close()

	time.Sleep(7 * time.Second) // the other reads trace_addresses every 5 s
	other.Add(agentSpan("/k8s/shop/basket", 2, tracev1.Span_SPAN_KIND_CLIENT, attr("server.address", "10.42.0.200"), attr("server.port", "8080")))
	other.Close()

	var server string
	require.NoError(t, c.QueryRow(ctx, "SELECT Server FROM @@table_otel_traces@@ WHERE TraceId = '"+fmt.Sprintf("%032x", 2)+"'").Scan(&server))
	assert.Equal(t, "shop/catalog", server)
}

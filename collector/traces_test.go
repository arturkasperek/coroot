package collector

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ClickHouse/ch-go"
	chproto "github.com/ClickHouse/ch-go/proto"
	"github.com/cespare/xxhash/v2"
	"github.com/stretchr/testify/require"
	v1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestExtractBodiesMovesThemOutOfAttributes(t *testing.T) {
	attrs := map[string]string{
		"http.method":        "POST",
		"http.request.body":  `{"id":1}`,
		"http.response.body": `{"ok":true}`,
	}
	req, resp := extractBodies(attrs)
	require.Equal(t, `{"id":1}`, req)
	require.Equal(t, `{"ok":true}`, resp)
	require.NotContains(t, attrs, "http.request.body", "a body left in SpanAttributes is read by every attribute filter")
	require.NotContains(t, attrs, "http.response.body")
	require.Equal(t, "POST", attrs["http.method"], "other attributes are untouched")
}

func TestExtractBodiesAbsent(t *testing.T) {
	attrs := map[string]string{"http.method": "GET"}
	req, resp := extractBodies(attrs)
	require.Empty(t, req)
	require.Empty(t, resp)
	require.Len(t, attrs, 1)
}

func TestExtractBodiesCapsSize(t *testing.T) {
	attrs := map[string]string{"http.request.body": strings.Repeat("x", maxSpanBodyBytes+100)}
	req, _ := extractBodies(attrs)
	require.Len(t, req, maxSpanBodyBytes)
}

func stringAttr(k, v string) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: v}}}
}

// savedRows reads the rows a batch wrote to a table, column by column, while
// the batch still holds them (it resets its columns after the write).
type savedRows map[string][]map[string]any

func recordSaves(rows savedRows) func(q ch.Query) error {
	return func(q ch.Query) error {
		table := "otel_traces"
		if strings.Contains(q.Body, "trace_bodies") {
			table = "trace_bodies"
		}
		for _, column := range q.Input {
			for index := 0; index < column.Data.Rows(); index++ {
				for len(rows[table]) <= index {
					rows[table] = append(rows[table], map[string]any{})
				}
				switch data := column.Data.(type) {
				case *chproto.ColUInt64:
					rows[table][index][column.Name] = data.Row(index)
				case *chproto.ColStr:
					rows[table][index][column.Name] = data.Row(index)
				case *chproto.ColUInt8:
					rows[table][index][column.Name] = data.Row(index)
				case *chproto.ColLowCardinality[string]:
					rows[table][index][column.Name] = data.Row(index)
				}
			}
		}
		return nil
	}
}

func spanWithBodies(name, request, response string) *tracev1.Span {
	attributes := []*commonv1.KeyValue{stringAttr("http.method", "POST")}
	if request != "" {
		attributes = append(attributes, stringAttr("http.request.body", request))
	}
	if response != "" {
		attributes = append(attributes, stringAttr("http.response.body", response))
	}
	return &tracev1.Span{TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: name, Attributes: attributes}
}

// directoryOf is the directory of a batch's project, built already.
func directoryOf(directory *ApplicationDirectory) func() (*ApplicationDirectory, bool) {
	return func() (*ApplicationDirectory, bool) { return directory, true }
}

func addSpans(b *TracesBatch, spans ...*tracev1.Span) {
	b.Add(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: spans}},
	}}})
}

// A body is written once, to trace_bodies, by its hash; the spans keep the
// hash. The two sides of a request and repeated answers share their body.
func TestTracesBatchStoresBodiesOnceByHash(t *testing.T) {
	rows := savedRows{}
	b := NewTracesBatch(100, time.Hour, recordSaves(rows), nil, nil)
	addSpans(b,
		spanWithBodies("client side", `{"order":1}`, `{"id":"ab"}`),
		spanWithBodies("server side", `{"order":1}`, `{"id":"ab"}`),
		spanWithBodies("another", `{"order":2}`, ""),
		spanWithBodies("no body", "", ""),
	)
	b.Close()

	require.Len(t, rows["otel_traces"], 4)
	for _, span := range rows["otel_traces"] {
		require.NotContains(t, span, "RequestBody")
		require.NotContains(t, span, "ResponseBody")
	}
	require.Equal(t, xxhash.Sum64String(`{"order":1}`), rows["otel_traces"][0]["RequestBodyHash"])
	require.Equal(t, xxhash.Sum64String(`{"id":"ab"}`), rows["otel_traces"][0]["ResponseBodyHash"])
	require.Equal(t, rows["otel_traces"][0]["RequestBodyHash"], rows["otel_traces"][1]["RequestBodyHash"])
	require.Equal(t, uint64(0), rows["otel_traces"][2]["ResponseBodyHash"], "no body, no hash")
	require.Equal(t, uint64(0), rows["otel_traces"][3]["RequestBodyHash"])

	bodies := map[uint64]string{}
	for _, body := range rows["trace_bodies"] {
		bodies[body["Hash"].(uint64)] = body["Body"].(string)
	}
	require.Equal(t, map[uint64]string{
		xxhash.Sum64String(`{"order":1}`): `{"order":1}`,
		xxhash.Sum64String(`{"id":"ab"}`): `{"id":"ab"}`,
		xxhash.Sum64String(`{"order":2}`): `{"order":2}`,
	}, bodies, "each body once")
}

// A body written a moment ago is not written again by the next batches; it is
// once it is old enough for its LastSeen to need moving.
func TestTracesBatchSkipsBodiesWrittenRecently(t *testing.T) {
	rows := savedRows{}
	b := NewTracesBatch(1, time.Hour, recordSaves(rows), nil, nil)
	defer b.Close()
	addSpans(b, spanWithBodies("first", `{"order":1}`, ""))
	require.Len(t, rows["trace_bodies"], 1)

	delete(rows, "trace_bodies")
	addSpans(b, spanWithBodies("second", `{"order":1}`, ""))
	require.Empty(t, rows["trace_bodies"])
	require.Len(t, rows["otel_traces"], 1, "the span is written")

	b.bodiesWrittenAt[xxhash.Sum64String(`{"order":1}`)] = time.Now().Add(-2 * bodyRewriteInterval)
	addSpans(b, spanWithBodies("third", `{"order":1}`, ""))
	require.Len(t, rows["trace_bodies"], 1, "written again to move its LastSeen")
}

// The client and the server spans of a request to a server that records its
// side are both written, and only the server's is counted.
func TestTracesBatchCountsTheServerSideOfAKnownServer(t *testing.T) {
	rows := savedRows{}
	directory := testApplicationDirectory()
	b := NewTracesBatch(100, time.Hour, recordSaves(rows), nil, directoryOf(directory))
	agentSpan := func(kind tracev1.Span_SpanKind, pod, containerName string, attributes ...*commonv1.KeyValue) *tracev1.ResourceSpans {
		return &tracev1.ResourceSpans{
			Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
				stringAttr("container.id", "/k8s/coroot-dev/"+pod+"/"+containerName),
			}},
			ScopeSpans: []*tracev1.ScopeSpans{{
				Scope: &commonv1.InstrumentationScope{Name: "coroot-node-agent"},
				Spans: []*tracev1.Span{{TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "GET", Kind: kind, Attributes: attributes}},
			}},
		}
	}
	b.Add(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{
		// express-demo's agent records the server side of a request from demo-traffic
		agentSpan(tracev1.Span_SPAN_KIND_SERVER, "express-demo-6c678767cd-5b9w7", "express", stringAttr("http.method", "GET"), stringAttr("client.address", "10.42.0.150")),
		// demo-traffic's agent records the client side of it
		agentSpan(tracev1.Span_SPAN_KIND_CLIENT, "demo-traffic-75b97975f8-vmwbw", "traffic", stringAttr("http.method", "GET"), stringAttr("server.address", "10.42.0.140"), stringAttr("server.port", "3000")),
		// a call to an external service has no server side
		agentSpan(tracev1.Span_SPAN_KIND_CLIENT, "demo-traffic-75b97975f8-vmwbw", "traffic", stringAttr("http.method", "POST"), stringAttr("server.address", "54.187.174.169"), stringAttr("server.port", "443")),
	}})
	b.Close()

	require.Len(t, rows["otel_traces"], 3)
	require.Equal(t, "coroot-dev/express-demo", rows["otel_traces"][0]["Server"])
	require.Equal(t, uint8(1), rows["otel_traces"][0]["Counted"], "the server's span counts the request")
	require.Equal(t, "coroot-dev/express-demo", rows["otel_traces"][1]["Server"])
	require.Equal(t, uint8(0), rows["otel_traces"][1]["Counted"], "the client's span of it is a second record")
	require.Equal(t, uint8(1), rows["otel_traces"][2]["Counted"], "an external service is counted by its client")
}

// The key the node agent gives a request (coroot.request.key, 16 hex digits)
// goes to the RequestKey column, the same at the client and the server spans.
func TestTracesBatchWritesRequestKey(t *testing.T) {
	rows := savedRows{}
	b := NewTracesBatch(100, time.Hour, recordSaves(rows), nil, nil)
	addSpans(b,
		&tracev1.Span{TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "GET",
			Attributes: []*commonv1.KeyValue{stringAttr("http.method", "GET"), stringAttr("coroot.request.key", "00000000deadbeef")}},
		&tracev1.Span{TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "GET",
			Attributes: []*commonv1.KeyValue{stringAttr("http.method", "GET"), stringAttr("coroot.request.key", "not hex")}},
		&tracev1.Span{TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "GET",
			Attributes: []*commonv1.KeyValue{stringAttr("http.method", "GET")}},
	)
	b.Close()
	require.Len(t, rows["otel_traces"], 3)
	require.Equal(t, uint64(0xdeadbeef), rows["otel_traces"][0]["RequestKey"])
	require.Equal(t, uint64(0), rows["otel_traces"][1]["RequestKey"], "a malformed key is no key")
	require.Equal(t, uint64(0), rows["otel_traces"][2]["RequestKey"])
}

// A restarted collector knows from trace_server_sources (the stored server
// spans of every Coroot replica) that express-demo's agent records the server
// side of HTTP: the first client span of a request to it, before any server
// span reached this collector, is not counted. Right after a start the tables
// are not ready yet: the batch holds its spans, without making the agents
// wait, until the sources are read.
func TestTracesBatchKnowsTheServersOfStoredSpans(t *testing.T) {
	rows := savedRows{}
	var lock sync.Mutex
	saves := recordSaves(rows)
	reads := 0
	b := newTracesBatch(100, 50*time.Millisecond, func(query ch.Query) error {
		lock.Lock()
		defer lock.Unlock()
		return saves(query)
	}, tracesBatchReads{serverSpanSources: func() ([]serverSpanSource, error) {
		if reads++; reads < 3 {
			return nil, errors.New("clickhouse tables not ready")
		}
		return []serverSpanSource{{server: "coroot-dev/express-demo", protocol: "http", fromAgent: true, lastSeen: time.Now().Add(-30 * time.Minute)}}, nil
	}}, directoryOf(testApplicationDirectory()))
	defer b.Close()
	added := time.Now()
	b.Add(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{
			stringAttr("container.id", "/k8s/coroot-dev/demo-traffic-75b97975f8-vmwbw/traffic"),
		}},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Scope: &commonv1.InstrumentationScope{Name: "coroot-node-agent"},
			Spans: []*tracev1.Span{{TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "GET", Kind: tracev1.Span_SPAN_KIND_CLIENT, Attributes: []*commonv1.KeyValue{
				stringAttr("http.method", "GET"), stringAttr("server.address", "10.42.0.140"), stringAttr("server.port", "3000"),
			}}},
		}},
	}}})
	require.Less(t, time.Since(added), 100*time.Millisecond, "the agent does not wait")

	require.Eventually(t, func() bool {
		lock.Lock()
		defer lock.Unlock()
		return len(rows["otel_traces"]) == 1
	}, 10*time.Second, 50*time.Millisecond)
	require.Greater(t, time.Since(added), serverSpanSourcesRetry, "held until the sources are read")
	lock.Lock()
	defer lock.Unlock()
	require.Equal(t, "coroot-dev/express-demo", rows["otel_traces"][0]["Server"])
	require.Equal(t, "http", rows["otel_traces"][0]["Protocol"])
	require.Equal(t, uint8(0), rows["otel_traces"][0]["Counted"], "the server's span counts the request")
}

// catalog started a moment ago: the world does not know its pod, but its agent's
// server span tells where it received a request, so the next client span to
// that address names it as its server, and its own client span names it as
// the client of its requests.
func TestTracesBatchNamesAPodTheWorldDoesNotKnowYet(t *testing.T) {
	rows := savedRows{}
	directory := testDirectoryWithNode()
	b := NewTracesBatch(100, time.Hour, recordSaves(rows), nil, directoryOf(directory))
	// the agent's service name is /k8s/<namespace>/<application>
	agentSpans := func(container string, kind tracev1.Span_SpanKind, attributes ...*commonv1.KeyValue) *v1.ExportTraceServiceRequest {
		parts := strings.Split(container, "/")
		serviceName := "/k8s/" + parts[2] + "/" + parts[4]
		return &v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
			Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{stringAttr("container.id", container), stringAttr("service.name", serviceName)}},
			ScopeSpans: []*tracev1.ScopeSpans{{
				Scope: &commonv1.InstrumentationScope{Name: "coroot-node-agent"},
				Spans: []*tracev1.Span{{TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "GET", Kind: kind, Attributes: append(attributes, stringAttr("http.method", "GET"))}},
			}},
		}}}
	}
	const catalog = "/k8s/coroot-dev/catalog-5d9c7b8f6-x2x9q/catalog"
	b.Add(agentSpans(catalog, tracev1.Span_SPAN_KIND_SERVER,
		stringAttr("server.address", "10.42.0.200"), stringAttr("server.port", "8080"), stringAttr("client.address", "10.42.0.150")))
	b.Add(agentSpans("/k8s/coroot-dev/demo-traffic-75b97975f8-vmwbw/traffic", tracev1.Span_SPAN_KIND_CLIENT,
		stringAttr("server.address", "10.42.0.200"), stringAttr("server.port", "8080"), stringAttr("network.local.address", "10.42.0.150")))
	b.Add(agentSpans(catalog, tracev1.Span_SPAN_KIND_CLIENT,
		stringAttr("server.address", "10.42.0.140"), stringAttr("server.port", "3000"), stringAttr("network.local.address", "10.42.0.200")))
	b.Add(agentSpans("/k8s/coroot-dev/express-demo-6c678767cd-5b9w7/express", tracev1.Span_SPAN_KIND_SERVER,
		stringAttr("server.address", "10.42.0.140"), stringAttr("server.port", "3000"), stringAttr("client.address", "10.42.0.200")))
	b.Close()

	require.Len(t, rows["otel_traces"], 4)
	require.Equal(t, "coroot-dev/catalog", rows["otel_traces"][0]["Server"], "a span's own side, from the service name")
	require.Equal(t, "coroot-dev/catalog", rows["otel_traces"][1]["Server"], "the address its server span received a request on")
	require.Equal(t, "coroot-dev/catalog", rows["otel_traces"][3]["Client"], "the address its client span was sent from")
	require.Contains(t, b.RecentClients(), "10.42.0.200", "a pod with an agent: a traced client")
}

// A pod that lives a moment (psql run once) makes all its requests within one
// batch, and an agent sends the spans of each container together: the server
// spans of postgres come before the client spans that tell where psql sent
// them from. The batch learns the addresses of all its spans before it names
// the peers, so the server spans name psql too.
func TestTracesBatchLearnsAddressesBeforeNamingPeers(t *testing.T) {
	rows := savedRows{}
	directory := testDirectoryWithNode()
	b := NewTracesBatch(100, time.Hour, recordSaves(rows), nil, directoryOf(directory))
	agentSpan := func(container, serviceName string, kind tracev1.Span_SpanKind, attributes ...*commonv1.KeyValue) *tracev1.ResourceSpans {
		return &tracev1.ResourceSpans{
			Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{stringAttr("container.id", container), stringAttr("service.name", serviceName)}},
			ScopeSpans: []*tracev1.ScopeSpans{{
				Scope: &commonv1.InstrumentationScope{Name: "coroot-node-agent"},
				Spans: []*tracev1.Span{{TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "query", Kind: kind,
					Attributes: append(attributes, stringAttr("db.system", "postgresql"))}},
			}},
		}
	}
	b.Add(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{
		agentSpan("/k8s/coroot-dev/postgres-6fbbcf454b-9klv2/postgres", "/k8s/coroot-dev/postgres", tracev1.Span_SPAN_KIND_SERVER,
			stringAttr("server.address", "10.42.0.20"), stringAttr("server.port", "5432"), stringAttr("client.address", "10.42.0.61")),
		agentSpan("/k8s/coroot-dev/psql-burst/psql-burst", "/k8s/coroot-dev/psql-burst/psql-burst", tracev1.Span_SPAN_KIND_CLIENT,
			stringAttr("server.address", "10.42.0.20"), stringAttr("server.port", "5432"), stringAttr("network.local.address", "10.42.0.61")),
	}})
	b.Close()

	require.Len(t, rows["otel_traces"], 2)
	require.Equal(t, "coroot-dev/psql-burst", rows["otel_traces"][0]["Client"], "named by the client span of the same batch")
	require.Equal(t, "coroot-dev/postgres", rows["otel_traces"][1]["Server"], "named by the server span of the same batch")
}

// Right after a start the application directory of the project is not built
// yet: the batch holds its spans until it is, without making the agents wait,
// so that their client and server are named by the world.
func TestTracesBatchWaitsForTheFirstDirectory(t *testing.T) {
	rows := savedRows{}
	var lock sync.Mutex
	saves := recordSaves(rows)
	var built atomic.Bool
	b := NewTracesBatch(100, 50*time.Millisecond, func(query ch.Query) error {
		lock.Lock()
		defer lock.Unlock()
		return saves(query)
	}, nil, func() (*ApplicationDirectory, bool) {
		if !built.Load() {
			return nil, false
		}
		return testApplicationDirectory(), true
	})
	defer b.Close()
	b.Add(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{stringAttr("service.name", "/k8s/coroot-dev/demo-traffic")}},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Scope: &commonv1.InstrumentationScope{Name: "coroot-node-agent"},
			Spans: []*tracev1.Span{{TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "GET", Kind: tracev1.Span_SPAN_KIND_CLIENT, Attributes: []*commonv1.KeyValue{
				stringAttr("http.method", "GET"), stringAttr("server.address", "10.42.0.140"), stringAttr("server.port", "3000"),
			}}},
		}},
	}}})
	time.Sleep(300 * time.Millisecond)
	lock.Lock()
	require.Empty(t, rows["otel_traces"], "held while there is no directory")
	lock.Unlock()

	built.Store(true)
	require.Eventually(t, func() bool {
		lock.Lock()
		defer lock.Unlock()
		return len(rows["otel_traces"]) == 1
	}, 5*time.Second, 50*time.Millisecond)
	lock.Lock()
	defer lock.Unlock()
	require.Equal(t, "coroot-dev/express-demo", rows["otel_traces"][0]["Server"], "named by the directory")
}

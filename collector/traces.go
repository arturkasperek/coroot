package collector

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ClickHouse/ch-go"
	chproto "github.com/ClickHouse/ch-go/proto"
	"github.com/cespare/xxhash/v2"
	"github.com/coroot/coroot/clickhouse"
	semconv "go.opentelemetry.io/collector/semconv/v1.18.0"
	v1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
	"k8s.io/klog"
)

const (
	attrHTTPRequestBody  = "http.request.body"
	attrHTTPResponseBody = "http.response.body"

	// Any OTLP client can send these, not only the node agent (which captures
	// at most 4 KB per message), so the collector caps them itself.
	maxSpanBodyBytes = 16 * 1024

	// bodyRewriteInterval is how long a body written to trace_bodies is not
	// written again. Writing it again moves its LastSeen, which its TTL counts
	// from, so a body still in use outlives the spans that carry it.
	bodyRewriteInterval = time.Hour
	// maxBodiesWritten bounds the hashes remembered for bodyRewriteInterval;
	// past it they are forgotten and the bodies written again.
	maxBodiesWritten = 100000
)

// maxHeldBatches bounds the spans a new batch holds until it knows the servers
// that record their side, in batches of its limit.
const maxHeldBatches = 10

// nodeAgentScope is the instrumentation scope of the spans of the node agent.
const nodeAgentScope = "coroot-node-agent"

// attrRequestKey is the key the node agent gives a request, the same at its
// client and server spans (tracing.RequestKey of the agent), as 16 hex digits.
const attrRequestKey = "coroot.request.key"

// extractRequestKey takes the request key out of the span attributes into its
// own column; 0 for none.
func extractRequestKey(attrs map[string]string) uint64 {
	value, ok := attrs[attrRequestKey]
	if !ok {
		return 0
	}
	delete(attrs, attrRequestKey)
	key, err := strconv.ParseUint(value, 16, 64)
	if err != nil {
		return 0
	}
	return key
}

// bodyHash is the key of a body in trace_bodies, 0 for no body.
func bodyHash(body string) uint64 {
	if body == "" {
		return 0
	}
	return xxhash.Sum64String(body)
}

// extractBodies takes the HTTP bodies out of the span attributes. They go to
// their own columns: a Map column is read whole for any key, so a body left in
// SpanAttributes is read by every attribute filter.
func extractBodies(attrs map[string]string) (string, string) {
	req, resp := attrs[attrHTTPRequestBody], attrs[attrHTTPResponseBody]
	delete(attrs, attrHTTPRequestBody)
	delete(attrs, attrHTTPResponseBody)
	return capBody(req), capBody(resp)
}

func capBody(s string) string {
	if len(s) > maxSpanBodyBytes {
		return s[:maxSpanBodyBytes]
	}
	return s
}

func (c *Collector) Traces(w http.ResponseWriter, r *http.Request) {
	project, err := c.getProject(r.Header.Get(ApiKeyHeader))
	if err != nil {
		klog.Errorln(err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	contentType := r.Header.Get("Content-Type")
	switch contentType {
	case "application/x-protobuf":
	default:
		http.Error(w, "unsupported content type: "+contentType, http.StatusBadRequest)
		return
	}

	decoder, err := getDecoder(r.Header.Get("Content-Encoding"), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	data, err := io.ReadAll(decoder)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	req := &v1.ExportTraceServiceRequest{}
	err = proto.Unmarshal(data, req)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	c.getTracesBatch(project).Add(req)

	resp := &v1.ExportTraceServiceResponse{}
	w.Header().Set("Content-Type", contentType)
	data, err = proto.Marshal(resp)
	if err != nil {
		klog.Errorln(err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

type TracesBatch struct {
	limit int
	exec  func(query ch.Query) error

	lock sync.Mutex
	done chan struct{}

	Timestamp          *chproto.ColDateTime64
	TraceId            *chproto.ColStr
	SpanId             *chproto.ColStr
	ParentSpanId       *chproto.ColStr
	TraceState         *chproto.ColStr
	SpanName           *chproto.ColLowCardinality[string]
	SpanKind           *chproto.ColLowCardinality[string]
	ServiceName        *chproto.ColLowCardinality[string]
	Client             *chproto.ColLowCardinality[string]
	Server             *chproto.ColLowCardinality[string]
	Counted            *chproto.ColUInt8
	Protocol           *chproto.ColLowCardinality[string]
	RequestKey         *chproto.ColUInt64
	ResourceAttributes *chproto.ColMap[string, string]
	SpanAttributes     *chproto.ColMap[string, string]
	Duration           *chproto.ColInt64
	StatusCode         *chproto.ColLowCardinality[string]
	StatusMessage      *chproto.ColStr
	RequestBodyHash    *chproto.ColUInt64
	ResponseBodyHash   *chproto.ColUInt64
	EventsTimestamp    *chproto.ColArr[time.Time]
	EventsName         *chproto.ColArr[string]
	EventsAttributes   *chproto.ColArr[map[string]string]
	LinksTraceId       *chproto.ColArr[string]
	LinksSpanId        *chproto.ColArr[string]
	LinksTraceState    *chproto.ColArr[string]
	LinksAttributes    *chproto.ColArr[map[string]string]

	// bodies are the bodies of the spans of the batch not written lately;
	// bodiesWrittenAt is when each body was last written.
	bodies          map[uint64]string
	bodiesWrittenAt map[uint64]time.Time

	// The applications on the two sides of a span (Client, Server) and whether
	// the views count it (Counted) are decided when the spans are written,
	// from pending: by then the batch knows the application directory of its
	// project (directory, ready once its first build is over), the servers
	// that record their side (servers) and the addresses of the pods the world
	// does not know yet (addresses), both read from ClickHouse too (each
	// loaded channel is closed once its read succeeded). Until it knows them
	// all, and at most serverSpanSourcesMaxHold after createdAt, a new batch
	// holds its spans; the agents never wait.
	pending         []pendingSpan
	directory       func() (*ApplicationDirectory, bool)
	servers         *serverSpanSources
	serversLoaded   chan struct{}
	addresses       *recentAddresses
	addressesLoaded chan struct{}
	createdAt       time.Time
}

// tracesBatchReads read from ClickHouse what the spans of every Coroot replica
// taught; nil functions read nothing.
type tracesBatchReads struct {
	serverSpanSources func() ([]serverSpanSource, error)
	recentAddresses   func() ([]recentAddressRow, error)
}

// pendingSpan is what the Client, Server and Counted columns of a span are
// decided from when it is written.
type pendingSpan struct {
	serviceName string
	spanKind    string
	fromAgent   bool
	protocol    string
	resource    map[string]string
	span        map[string]string
}

// NewTracesBatch makes the batch of a project: exec writes to its ClickHouse,
// reader gives the client that reads from it the server span sources and the
// addresses of new pods (nil: none are read, the batch learns them from its
// spans only), directory the application directory of the project and
// whether its first build is over (nil: none).
func NewTracesBatch(limit int, timeout time.Duration, exec func(query ch.Query) error, reader func() (*clickhouse.Client, error), directory func() (*ApplicationDirectory, bool)) *TracesBatch {
	if reader == nil {
		return newTracesBatch(limit, timeout, exec, tracesBatchReads{}, directory)
	}
	return newTracesBatch(limit, timeout, exec, tracesBatchReads{
		serverSpanSources: readWith(reader, loadServerSpanSources),
		recentAddresses:   readWith(reader, loadRecentAddresses),
	}, directory)
}

// readWith makes a read from ClickHouse with the client reader gives, bounded
// by serverSpanSourcesQueryTimeout.
func readWith[T any](reader func() (*clickhouse.Client, error), read func(context.Context, *clickhouse.Client) (T, error)) func() (T, error) {
	return func() (T, error) {
		client, err := reader()
		if err != nil {
			var none T
			return none, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), serverSpanSourcesQueryTimeout)
		defer cancel()
		return read(ctx, client)
	}
}

// newTracesBatch is NewTracesBatch with the functions reading from ClickHouse.
func newTracesBatch(limit int, timeout time.Duration, exec func(query ch.Query) error, reads tracesBatchReads, directory func() (*ApplicationDirectory, bool)) *TracesBatch {
	if directory == nil {
		directory = func() (*ApplicationDirectory, bool) { return nil, true }
	}
	b := &TracesBatch{
		limit: limit,
		exec:  exec,
		done:  make(chan struct{}),

		Timestamp:          new(chproto.ColDateTime64).WithPrecision(chproto.PrecisionNano),
		TraceId:            new(chproto.ColStr),
		SpanId:             new(chproto.ColStr),
		ParentSpanId:       new(chproto.ColStr),
		TraceState:         new(chproto.ColStr),
		SpanName:           new(chproto.ColStr).LowCardinality(),
		SpanKind:           new(chproto.ColStr).LowCardinality(),
		ServiceName:        new(chproto.ColStr).LowCardinality(),
		Client:             new(chproto.ColStr).LowCardinality(),
		Server:             new(chproto.ColStr).LowCardinality(),
		Counted:            new(chproto.ColUInt8),
		Protocol:           new(chproto.ColStr).LowCardinality(),
		RequestKey:         new(chproto.ColUInt64),
		ResourceAttributes: chproto.NewMap[string, string](new(chproto.ColStr).LowCardinality(), new(chproto.ColStr)),
		SpanAttributes:     chproto.NewMap[string, string](new(chproto.ColStr).LowCardinality(), new(chproto.ColStr)),
		Duration:           new(chproto.ColInt64),
		StatusCode:         new(chproto.ColStr).LowCardinality(),
		StatusMessage:      new(chproto.ColStr),
		RequestBodyHash:    new(chproto.ColUInt64),
		ResponseBodyHash:   new(chproto.ColUInt64),
		EventsTimestamp:    new(chproto.ColDateTime64).WithPrecision(chproto.PrecisionNano).Array(),
		EventsName:         new(chproto.ColStr).LowCardinality().Array(),
		EventsAttributes:   chproto.NewArray[map[string]string](chproto.NewMap[string, string](new(chproto.ColStr).LowCardinality(), new(chproto.ColStr))),
		LinksTraceId:       new(chproto.ColStr).Array(),
		LinksSpanId:        new(chproto.ColStr).Array(),
		LinksTraceState:    new(chproto.ColStr).Array(),
		LinksAttributes:    chproto.NewArray[map[string]string](chproto.NewMap[string, string](new(chproto.ColStr).LowCardinality(), new(chproto.ColStr))),

		bodies:          map[uint64]string{},
		bodiesWrittenAt: map[uint64]time.Time{},
		directory:       directory,
		servers:         newServerSpanSources(),
		serversLoaded:   make(chan struct{}),
		addresses:       newRecentAddresses(),
		addressesLoaded: make(chan struct{}),
		createdAt:       time.Now(),
	}

	if reads.serverSpanSources == nil {
		close(b.serversLoaded)
	} else {
		go b.keepReading("server span sources", serverSpanSourcesRefresh, b.serversLoaded, func() error {
			sources, err := reads.serverSpanSources()
			if err == nil {
				b.servers.remember(sources)
			}
			return err
		})
	}
	if reads.recentAddresses == nil {
		close(b.addressesLoaded)
	} else {
		go b.keepReading("recent addresses", recentAddressesRefresh, b.addressesLoaded, func() error {
			rows, err := reads.recentAddresses()
			if err == nil {
				directory, _ := b.directory()
				b.addresses.remember(directory, rows)
			}
			return err
		})
	}

	go func() {
		ticker := time.NewTicker(timeout)
		defer ticker.Stop()
		for {
			select {
			case <-b.done:
				return
			case <-ticker.C:
				b.lock.Lock()
				b.save()
				b.lock.Unlock()
			}
		}
	}()

	return b
}

// Close writes what the batch holds, once what it reads from ClickHouse is
// read (at most serverSpanSourcesMaxHold after the batch was made), with the
// directory known by then.
func (b *TracesBatch) Close() {
	deadline := time.After(time.Until(b.createdAt.Add(serverSpanSourcesMaxHold)))
	for _, loaded := range []chan struct{}{b.serversLoaded, b.addressesLoaded} {
		select {
		case <-loaded:
		case <-deadline:
		}
	}
	close(b.done) // stops the saving and the reading from ClickHouse
	b.lock.Lock()
	defer b.lock.Unlock()
	b.write()
}

// keepReading reads something from ClickHouse with read, every
// serverSpanSourcesRetry until it succeeds once (closing loaded then), then
// every refresh, until the batch is closed.
func (b *TracesBatch) keepReading(what string, refresh time.Duration, loaded chan struct{}, read func() error) {
	succeeded := false
	for {
		if err := read(); err != nil {
			klog.Warningln(what+":", err)
		} else if !succeeded {
			succeeded = true
			close(loaded)
		}
		interval := refresh
		if !succeeded {
			interval = serverSpanSourcesRetry
		}
		select {
		case <-b.done:
			return
		case <-time.After(interval):
		}
	}
}

// readsDone tells whether what the batch reads from ClickHouse was read once.
func (b *TracesBatch) readsDone() bool {
	for _, loaded := range []chan struct{}{b.serversLoaded, b.addressesLoaded} {
		select {
		case <-loaded:
		default:
			return false
		}
	}
	return true
}

// Add adds the spans of a request.
func (b *TracesBatch) Add(req *v1.ExportTraceServiceRequest) {
	b.lock.Lock()
	defer b.lock.Unlock()

	for _, rs := range req.GetResourceSpans() {
		var serviceName string
		resourceAttributes := attributesToMap(rs.GetResource().GetAttributes())
		for k, v := range resourceAttributes {
			if k == semconv.AttributeServiceName {
				serviceName = v
			}
		}
		for _, ss := range rs.GetScopeSpans() {
			scopeName := ss.GetScope().GetName()
			scopeVersion := ss.GetScope().GetVersion()
			for _, s := range ss.GetSpans() {
				spanAttributes := attributesToMap(s.GetAttributes())
				requestBody, responseBody := extractBodies(spanAttributes)
				requestKey := extractRequestKey(spanAttributes)
				if scopeName != "" {
					spanAttributes[semconv.AttributeOtelScopeName] = scopeName
				}
				if scopeVersion != "" {
					spanAttributes[semconv.AttributeOtelScopeVersion] = scopeVersion
				}
				var eventTimestamps []time.Time
				var eventNames []string
				var eventAttributes []map[string]string
				for _, e := range s.GetEvents() {
					eventTimestamps = append(eventTimestamps, time.Unix(0, int64(e.GetTimeUnixNano())))
					eventNames = append(eventNames, e.GetName())
					eventAttributes = append(eventAttributes, attributesToMap(e.GetAttributes()))
				}
				var linkTraceIds []string
				var linkSpanIds []string
				var linkTraceStates []string
				var linkAttributes []map[string]string
				for _, l := range s.GetLinks() {
					linkTraceIds = append(linkTraceIds, hex.EncodeToString(l.GetTraceId()))
					linkSpanIds = append(linkSpanIds, hex.EncodeToString(l.GetSpanId()))
					linkTraceStates = append(linkTraceStates, l.GetTraceState())
					linkAttributes = append(linkAttributes, attributesToMap(l.GetAttributes()))
				}

				b.Timestamp.Append(time.Unix(0, int64(s.GetStartTimeUnixNano())))
				b.TraceId.Append(hex.EncodeToString(s.GetTraceId()))
				b.SpanId.Append(hex.EncodeToString(s.GetSpanId()))
				b.ParentSpanId.Append(hex.EncodeToString(s.GetParentSpanId()))
				b.TraceState.Append(s.GetTraceState())
				b.SpanName.Append(s.GetName())
				b.SpanKind.Append(s.GetKind().String())
				b.ServiceName.Append(serviceName)
				fromAgent := scopeName == nodeAgentScope
				protocol := requestProtocol(spanAttributes)
				b.Protocol.Append(protocol)
				b.pending = append(b.pending, pendingSpan{
					serviceName: serviceName,
					spanKind:    s.GetKind().String(),
					fromAgent:   fromAgent,
					protocol:    protocol,
					resource:    resourceAttributes,
					span:        spanAttributes,
				})
				b.ResourceAttributes.Append(resourceAttributes)
				b.SpanAttributes.Append(spanAttributes)
				b.Duration.Append(int64(s.GetEndTimeUnixNano() - s.GetStartTimeUnixNano()))
				b.StatusCode.Append(s.GetStatus().GetCode().String())
				b.StatusMessage.Append(s.GetStatus().GetMessage())
				b.RequestKey.Append(requestKey)
				b.RequestBodyHash.Append(b.addBody(requestBody))
				b.ResponseBodyHash.Append(b.addBody(responseBody))
				b.EventsTimestamp.Append(eventTimestamps)
				b.EventsName.Append(eventNames)
				b.EventsAttributes.Append(eventAttributes)
				b.LinksTraceId.Append(linkTraceIds)
				b.LinksSpanId.Append(linkSpanIds)
				b.LinksTraceState.Append(linkTraceStates)
				b.LinksAttributes.Append(linkAttributes)
			}
		}
	}
	if b.Timestamp.Rows() < b.limit {
		return
	}
	b.save()
}

// learnAddresses records the addresses a span of the node agent tells of its
// own side (client and server are the span's, as the directory names them).
func (b *TracesBatch) learnAddresses(directory *ApplicationDirectory, span pendingSpan, client, server string, now time.Time) {
	if !span.fromAgent {
		return
	}
	switch span.spanKind {
	case "SPAN_KIND_SERVER":
		b.addresses.learn(directory, span.spanKind, server, span.span, now)
	case "SPAN_KIND_CLIENT":
		b.addresses.learn(directory, span.spanKind, client, span.span, now)
	}
}

// namePeers names a peer the directory does not know, which it names by its
// address, by the recent addresses of the pods the world does not know yet.
func (b *TracesBatch) namePeers(span pendingSpan, client, server string, now time.Time) (string, string) {
	switch span.spanKind {
	case "SPAN_KIND_SERVER", "SPAN_KIND_CONSUMER":
		if address := clientAddress(span.span); address != "" && client == address {
			if name, ok := b.addresses.lookup(address, now); ok {
				client = name
			}
		}
	case "SPAN_KIND_CLIENT", "SPAN_KIND_PRODUCER":
		if address := serverAddress(span.span); address != "" && server == address {
			if name, ok := b.addresses.lookup(address, now); ok {
				server = name
			}
		}
	}
	return client, server
}

// RecentClients are the IPs of the pods with a node agent the world does not
// know yet: traced clients, as those of the directory.
func (b *TracesBatch) RecentClients() []string {
	return b.addresses.clients(time.Now())
}

// addBody keeps a body to write with the batch, unless it was written lately,
// and returns its hash.
func (b *TracesBatch) addBody(body string) uint64 {
	hash := bodyHash(body)
	if hash == 0 {
		return 0
	}
	if writtenAt, ok := b.bodiesWrittenAt[hash]; ok && time.Since(writtenAt) < bodyRewriteInterval {
		return hash
	}
	b.bodies[hash] = body
	return hash
}

// saveBodies writes the bodies of the batch to trace_bodies before its spans,
// so that a span never refers to a body not written yet.
func (b *TracesBatch) saveBodies() {
	if len(b.bodies) == 0 {
		return
	}
	now := time.Now()
	hashes := new(chproto.ColUInt64)
	bodies := new(chproto.ColStr)
	lastSeen := new(chproto.ColDateTime)
	for hash, body := range b.bodies {
		hashes.Append(hash)
		bodies.Append(body)
		lastSeen.Append(now)
	}
	input := chproto.Input{
		{Name: "Hash", Data: hashes},
		{Name: "Body", Data: bodies},
		{Name: "LastSeen", Data: lastSeen},
	}
	if err := b.exec(ch.Query{Body: input.Into("@@table_trace_bodies@@"), Input: input}); err != nil {
		klog.Errorln(err)
		b.bodies = map[uint64]string{}
		return // not remembered as written: the next batch that has them writes them
	}
	if len(b.bodiesWrittenAt)+len(b.bodies) > maxBodiesWritten {
		b.bodiesWrittenAt = map[uint64]time.Time{}
	}
	for hash := range b.bodies {
		b.bodiesWrittenAt[hash] = now
	}
	b.bodies = map[uint64]string{}
}

// save writes the spans of the batch, unless it is a new batch that does not
// know yet its directory and what it reads from ClickHouse: it holds them
// then, at most serverSpanSourcesMaxHold and maxHeldBatches.
func (b *TracesBatch) save() {
	_, directoryReady := b.directory()
	if !(b.readsDone() && directoryReady) && time.Since(b.createdAt) < serverSpanSourcesMaxHold && b.Timestamp.Rows() < maxHeldBatches*b.limit {
		return
	}
	b.write()
}

// write writes the spans of the batch, naming their applications and deciding
// which of them the views count.
func (b *TracesBatch) write() {
	if b.Timestamp.Rows() == 0 {
		return
	}
	directory, _ := b.directory()
	now := time.Now()
	// The applications as the directory names them, and the addresses the
	// spans tell, all learned before any peer is named by them: an agent sends
	// the spans of each container together, so the server spans of a request
	// can come before the client span that tells where it was sent from.
	names := make([][2]string, len(b.pending))
	for index, span := range b.pending {
		client, server := directory.SpanApplications(span.serviceName, span.spanKind, span.resource, span.span)
		names[index] = [2]string{client, server}
		b.learnAddresses(directory, span, client, server, now)
	}
	for index, span := range b.pending {
		client, server := b.namePeers(span, names[index][0], names[index][1], now)
		b.Client.Append(client)
		b.Server.Append(server)
		if b.servers.counted(span.spanKind, span.fromAgent, server, span.protocol, now) {
			b.Counted.Append(1)
		} else {
			b.Counted.Append(0)
		}
	}
	b.pending = b.pending[:0]
	b.saveBodies()

	input := chproto.Input{
		{Name: "Timestamp", Data: b.Timestamp},
		{Name: "TraceId", Data: b.TraceId},
		{Name: "SpanId", Data: b.SpanId},
		{Name: "ParentSpanId", Data: b.ParentSpanId},
		{Name: "TraceState", Data: b.TraceState},
		{Name: "SpanName", Data: b.SpanName},
		{Name: "SpanKind", Data: b.SpanKind},
		{Name: "ServiceName", Data: b.ServiceName},
		{Name: "Client", Data: b.Client},
		{Name: "Server", Data: b.Server},
		{Name: "Counted", Data: b.Counted},
		{Name: "Protocol", Data: b.Protocol},
		{Name: "RequestKey", Data: b.RequestKey},
		{Name: "ResourceAttributes", Data: b.ResourceAttributes},
		{Name: "SpanAttributes", Data: b.SpanAttributes},
		{Name: "Duration", Data: b.Duration},
		{Name: "StatusCode", Data: b.StatusCode},
		{Name: "StatusMessage", Data: b.StatusMessage},
		{Name: "RequestBodyHash", Data: b.RequestBodyHash},
		{Name: "ResponseBodyHash", Data: b.ResponseBodyHash},
		{Name: "Events.Timestamp", Data: b.EventsTimestamp},
		{Name: "Events.Name", Data: b.EventsName},
		{Name: "Events.Attributes", Data: b.EventsAttributes},
		{Name: "Links.TraceId", Data: b.LinksTraceId},
		{Name: "Links.SpanId", Data: b.LinksSpanId},
		{Name: "Links.TraceState", Data: b.LinksTraceState},
		{Name: "Links.Attributes", Data: b.LinksAttributes},
	}
	err := b.exec(ch.Query{Body: input.Into("@@table_otel_traces@@"), Input: input})
	if err != nil {
		klog.Errorln(err)
	}
	for _, i := range input {
		i.Data.(chproto.Resettable).Reset()
	}
}

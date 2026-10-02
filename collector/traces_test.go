package collector

import (
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/stretchr/testify/require"
	v1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
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

func TestTracesBatchWritesBodyColumns(t *testing.T) {
	var saved ch.Query
	b := NewTracesBatch(1, time.Hour, func(q ch.Query) error { saved = q; return nil })
	defer b.Close()
	b.Add(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
			TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "POST",
			Attributes: []*commonv1.KeyValue{
				stringAttr("http.method", "POST"),
				stringAttr("http.request.body", "req-body"),
				stringAttr("http.response.body", "resp-body"),
			},
		}}}},
	}}})

	// limit 1: Add saved the batch synchronously
	var names []string
	for _, c := range saved.Input {
		names = append(names, c.Name)
	}
	require.Contains(t, names, "RequestBody")
	require.Contains(t, names, "ResponseBody")
	require.Contains(t, saved.Body, "RequestBody")
}

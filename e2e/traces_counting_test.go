//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// A request recorded at both sides has two spans, of which the collector marks
// one counted. The Traces page lists and counts it once; a call to a server
// that records nothing at its side is counted by its client.
func TestOverviewCountsEachRequestOnce(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	namespace := "e2ecount-" + token
	server := namespace + "/orders"
	client := namespace + "/checkout"
	span := "GET /orders-" + token
	external := "POST /charges-" + token

	insertTraceFixture(t, []traceFixtureRow{
		// 10 requests from checkout to orders, recorded at both sides
		{Count: 10, ServiceName: "orders", SpanName: span, Kind: "SPAN_KIND_SERVER", Client: client, Server: server},
		{Count: 10, ServiceName: "/k8s/" + client, SpanName: span, Kind: "SPAN_KIND_CLIENT", Client: client, Server: server, NotCounted: true},
		// 3 calls of checkout to an external service, recorded at the client only
		{Count: 3, ServiceName: "/k8s/" + client, SpanName: external, Kind: "SPAN_KIND_CLIENT", Client: client, Server: "api.example.com:443"},
	})
	t.Cleanup(func() { deleteTraceFixture(t, "orders", "/k8s/"+client) })

	projectID := defaultProjectID(t)
	query := func(spanName string) map[string]any {
		return queryWithFilters(map[string]any{"view": "traces", "include_aux": true},
			map[string]string{"field": "SpanName", "op": "=", "value": spanName})
	}
	var traces overviewTraces
	waitUntil(t, 30*time.Second, "the counting fixture", func() bool {
		traces = fetchOverviewTraces(t, projectID, query(span))
		return traces.Error == "" && chartTotal(traces.RequestsChart, "other") == 10
	})
	if traces.Error != "" {
		t.Fatalf("overview traces error: %s", traces.Error)
	}

	t.Run("a request recorded at both sides once", func(t *testing.T) {
		if n := len(traces.Traces); n != 10 {
			t.Fatalf("the list has %d spans, want 10", n)
		}
		for _, s := range traces.Traces {
			if s.Service != "orders" {
				t.Fatalf("the list has a span of %s, want the server's spans only", s.Service)
			}
		}
		assertTraceFacet(t, traces, "Server", server, 10)
		assertTraceFacet(t, traces, "Client", client, 10)
		if got := chartTotal(traces.RequestsChart, "other"); got != 10 {
			t.Fatalf("requests chart counts %v, want 10", got)
		}
	})

	t.Run("a call to a server with no side of its own is counted by its client", func(t *testing.T) {
		got := fetchOverviewTraces(t, projectID, query(external))
		if n := len(got.Traces); n != 3 {
			t.Fatalf("the list has %d spans, want 3", n)
		}
		assertTraceFacet(t, got, "Server", "api.example.com:443", 3)
		if total := chartTotal(got.RequestsChart, "other"); total != 3 {
			t.Fatalf("requests chart counts %v, want 3", total)
		}
	})
}

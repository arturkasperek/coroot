//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// The traces page lists the root spans of applications instrumented with
// OpenTelemetry and the spans the node-agent makes from eBPF (their service
// name is the container id, which starts with a slash). The Source filter and
// facet tell them apart.
func TestOverviewTraceListIncludesAgentSpans(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	otelSvc := "e2eotel-" + token
	agentSvc := "/k8s/e2e-src-" + token + "/web"
	span := "GET /e2e/source-" + token

	insertTraceFixture(t, []traceFixtureRow{
		{Count: 7, ServiceName: otelSvc, SpanName: span},
		{Count: 4, ServiceName: agentSvc, SpanName: span},
		{Count: 3, ServiceName: agentSvc, SpanName: span + "-child", ParentSpanId: "deadbeefdeadbeef"}, // not a root span
	})
	t.Cleanup(func() { deleteTraceFixture(t, otelSvc, agentSvc) })

	projectID := defaultProjectID(t)
	query := func(filters ...map[string]string) map[string]any {
		fs := []map[string]string{{"field": "SpanName", "op": "=", "value": span}}
		return map[string]any{"view": "traces", "include_aux": true, "filters": append(fs, filters...)}
	}
	count := func(traces overviewTraces, service string) int {
		n := 0
		for _, s := range traces.Traces {
			if s.Service == service {
				n++
			}
		}
		return n
	}

	var traces overviewTraces
	waitUntil(t, 20*time.Second, "agent and OpenTelemetry spans in the list", func() bool {
		traces = fetchOverviewTraces(t, projectID, query())
		return traces.Error == "" && count(traces, otelSvc) == 7 && count(traces, agentSvc) == 4
	})
	if traces.Error != "" {
		t.Fatalf("overview traces error: %s", traces.Error)
	}
	assertTraceFacet(t, traces, "Source", "agent", 4)
	assertTraceFacet(t, traces, "Source", "otel", 7)

	t.Run("Source = agent lists only the agent spans", func(t *testing.T) {
		traces := fetchOverviewTraces(t, projectID, query(map[string]string{"field": "Source", "op": "=", "value": "agent"}))
		if got := count(traces, agentSvc); got != 4 {
			t.Fatalf("agent spans listed: %d want 4", got)
		}
		if got := count(traces, otelSvc); got != 0 {
			t.Fatalf("OpenTelemetry spans listed with Source=agent: %d", got)
		}
	})
	t.Run("Source = otel lists only the OpenTelemetry spans", func(t *testing.T) {
		traces := fetchOverviewTraces(t, projectID, query(map[string]string{"field": "Source", "op": "=", "value": "otel"}))
		if got := count(traces, otelSvc); got != 7 {
			t.Fatalf("OpenTelemetry spans listed: %d want 7", got)
		}
		if got := count(traces, agentSvc); got != 0 {
			t.Fatalf("agent spans listed with Source=otel: %d", got)
		}
	})
	t.Run("Source != agent equals Source = otel", func(t *testing.T) {
		traces := fetchOverviewTraces(t, projectID, query(map[string]string{"field": "Source", "op": "!=", "value": "agent"}))
		if count(traces, otelSvc) != 7 || count(traces, agentSvc) != 0 {
			t.Fatalf("otel=%d agent=%d", count(traces, otelSvc), count(traces, agentSvc))
		}
	})
	t.Run("the overview view counts them too", func(t *testing.T) {
		q := query()
		q["view"] = "overview"
		traces := fetchOverviewTraces(t, projectID, q)
		assertTraceFacet(t, traces, "ServiceName", agentSvc, 4)
		assertTraceFacet(t, traces, "ServiceName", otelSvc, 7)
	})
}

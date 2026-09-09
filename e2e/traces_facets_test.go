//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

func TestOverviewTraceFacetCounts(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	svcA := "e2ea-" + token
	svcB := "e2eb-" + token
	hello := "e2ehello-" + token
	errSpan := "e2eerror-" + token
	child := "e2echild-" + token
	agent := "/e2eagent-" + token

	insertTraceFixture(t, []traceFixtureRow{
		{Count: 30, ServiceName: svcA, SpanName: hello},
		{Count: 10, ServiceName: svcA, SpanName: errSpan},
		{Count: 8, ServiceName: svcB, SpanName: hello},
		{Count: 2, ServiceName: svcB, SpanName: errSpan},
		{Count: 5, ServiceName: svcA, SpanName: child, ParentSpanId: "deadbeefdeadbeef"},
		{Count: 5, ServiceName: agent, SpanName: hello},
	})
	t.Cleanup(func() { deleteTraceFixture(t, svcA, svcB, agent) })

	projectID := defaultProjectID(t)
	base := map[string]any{
		"view":        "overview",
		"include_aux": true,
	}

	var traces overviewTraces
	waitUntil(t, 15*time.Second, "overview trace facet counts for fixture", func() bool {
		traces = fetchOverviewTraces(t, projectID, base)
		return traces.Error == "" && facetValueCount(traces.Facets, "ServiceName", svcA) == 40
	})
	if traces.Error != "" {
		t.Fatalf("overview traces error: %s", traces.Error)
	}
	assertTraceFacet(t, traces, "ServiceName", svcA, 40)
	assertTraceFacet(t, traces, "ServiceName", svcB, 10)
	assertTraceFacet(t, traces, "ServiceName", agent, 0)
	assertTraceFacet(t, traces, "SpanName", hello, 38)
	assertTraceFacet(t, traces, "SpanName", errSpan, 12)
	assertTraceFacet(t, traces, "SpanName", child, 0)

	withSvc := copyQuery(base)
	withSvc["filters"] = []map[string]string{{"field": "ServiceName", "op": "=", "value": svcA}}
	traces = fetchOverviewTraces(t, projectID, withSvc)
	assertTraceFacet(t, traces, "ServiceName", svcA, 40)
	assertTraceFacet(t, traces, "ServiceName", svcB, 10)
	assertTraceFacet(t, traces, "SpanName", hello, 30)
	assertTraceFacet(t, traces, "SpanName", errSpan, 10)

	withSpan := copyQuery(base)
	withSpan["filters"] = []map[string]string{{"field": "SpanName", "op": "=", "value": hello}}
	traces = fetchOverviewTraces(t, projectID, withSpan)
	assertTraceFacet(t, traces, "SpanName", hello, 38)
	assertTraceFacet(t, traces, "SpanName", errSpan, 12)
	assertTraceFacet(t, traces, "ServiceName", svcA, 30)
	assertTraceFacet(t, traces, "ServiceName", svcB, 8)
}

func assertTraceFacet(t *testing.T, traces overviewTraces, key, value string, want uint64) {
	t.Helper()
	got := facetValueCount(traces.Facets, key, value)
	if got != want {
		t.Fatalf("facet %s=%s count=%d want %d facets=%v", key, value, got, want, traces.Facets)
	}
}

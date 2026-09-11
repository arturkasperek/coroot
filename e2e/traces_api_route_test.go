//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

func TestOverviewTraceApiRouteFilters(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	svcA := "e2eroute-a-" + token
	svcB := "e2eroute-b-" + token
	span := "e2eroute-span-" + token
	child := "e2eroute-child-" + token
	helloPath := "/e2e/route-hello-" + token
	metricsPath := "/e2e/route-metrics-" + token
	hello := "GET " + helloPath
	metrics := "GET " + metricsPath

	insertTraceFixture(t, []traceFixtureRow{
		{Count: 20, ServiceName: svcA, SpanName: span, SpanAttributes: map[string]string{"http.method": "GET", "http.target": helloPath + "?token=1"}},
		{Count: 8, ServiceName: svcB, SpanName: span, SpanAttributes: map[string]string{"http.method": "GET", "http.route": metricsPath, "http.target": metricsPath + "?foo=1"}},
		{Count: 5, ServiceName: svcA, SpanName: child, ParentSpanId: "deadbeefdeadbeef", SpanAttributes: map[string]string{"http.method": "GET", "http.target": helloPath}},
	})
	t.Cleanup(func() { deleteTraceFixture(t, svcA, svcB) })

	projectID := defaultProjectID(t)
	base := map[string]any{
		"view":        "traces",
		"include_aux": true,
	}

	var traces overviewTraces
	deadline := time.Now().Add(15 * time.Second)
	for {
		traces = fetchOverviewTraces(t, projectID, base)
		if traces.Error == "" && facetValueCount(traces.Facets, "ApiRoute", hello) == 20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for API route fixture error=%q facets=%v", traces.Error, traces.Facets)
		}
		time.Sleep(2 * time.Second)
	}
	assertTraceFacet(t, traces, "ApiRoute", hello, 20)
	assertTraceFacet(t, traces, "ApiRoute", metrics, 8)
	assertTraceFacet(t, traces, "ServiceName", svcA, 20)
	assertTraceFacet(t, traces, "ServiceName", svcB, 8)

	withRoute := copyQuery(base)
	withRoute["filters"] = []map[string]string{{"field": "ApiRoute", "op": "=", "value": hello}}
	traces = fetchOverviewTraces(t, projectID, withRoute)
	assertTraceFacet(t, traces, "ApiRoute", hello, 20)
	assertTraceFacet(t, traces, "ApiRoute", metrics, 8)
	assertTraceFacet(t, traces, "ServiceName", svcA, 20)
	assertTraceFacet(t, traces, "ServiceName", svcB, 0)
	for _, s := range traces.Traces {
		if s.Service == svcB {
			t.Fatalf("ApiRoute=%s still listed %s", hello, svcB)
		}
	}
}

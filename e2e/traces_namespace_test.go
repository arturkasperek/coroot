//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

func TestOverviewTraceNamespaceFilters(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	nsA := "e2ens-" + token
	svcA := "e2ens-a-" + token
	svcB := "e2ens-b-" + token
	span := "e2ens-span-" + token
	child := "e2ens-child-" + token

	insertTraceFixture(t, []traceFixtureRow{
		{Count: 20, ServiceName: svcA, SpanName: span, ResourceAttributes: map[string]string{"k8s.namespace.name": nsA}},
		{Count: 8, ServiceName: svcB, SpanName: span},
		{Count: 5, ServiceName: svcA, SpanName: child, ParentSpanId: "deadbeefdeadbeef", ResourceAttributes: map[string]string{"k8s.namespace.name": nsA}},
	})
	t.Cleanup(func() { deleteTraceFixture(t, svcA, svcB) })

	projectID := defaultProjectID(t)
	base := map[string]any{
		"view":        "traces",
		"include_aux": true,
	}

	var traces overviewTraces
	waitUntil(t, 15*time.Second, "overview traces with namespace fixture", func() bool {
		traces = fetchOverviewTraces(t, projectID, base)
		return traces.Error == "" && facetValueCount(traces.Facets, "Namespace", nsA) == 20
	})
	if traces.Error != "" {
		t.Fatalf("overview traces error: %s", traces.Error)
	}
	assertTraceFacet(t, traces, "Namespace", nsA, 20)
	assertTraceFacet(t, traces, "ServiceName", svcA, 20)
	assertTraceFacet(t, traces, "ServiceName", svcB, 8)

	withNS := copyQuery(base)
	withNS["filters"] = []map[string]string{{"field": "Namespace", "op": "=", "value": nsA}}
	traces = fetchOverviewTraces(t, projectID, withNS)
	assertTraceFacet(t, traces, "Namespace", nsA, 20)
	assertTraceFacet(t, traces, "ServiceName", svcA, 20)
	assertTraceFacet(t, traces, "ServiceName", svcB, 0)
	for _, s := range traces.Traces {
		if s.Service == svcB {
			t.Fatalf("Namespace=%s still listed %s", nsA, svcB)
		}
	}

	withSvc := copyQuery(base)
	withSvc["filters"] = []map[string]string{{"field": "ServiceName", "op": "=", "value": svcB}}
	traces = fetchOverviewTraces(t, projectID, withSvc)
	assertTraceFacet(t, traces, "Namespace", "n/a", 8)
	assertTraceFacet(t, traces, "ServiceName", svcB, 8)
	assertTraceFacet(t, traces, "ServiceName", svcA, 20)
}

//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// Two root populations with independent Namespace / ServiceName / ApiRoute / SpanName.
// Filtered field keeps unfiltered counts (skip-self, same as logs Severity).
// Every other group must shrink to the matching subset — including after ApiRoute,
// which is derived SQL and is not on the traces histogram MV.
func TestOverviewTraceFacetFiltersUpdateOtherGroups(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	nsA := "e2efx-ns-a-" + token
	nsB := "e2efx-ns-b-" + token
	svcA := "e2efx-svc-a-" + token
	svcB := "e2efx-svc-b-" + token
	spanA := "e2efx-span-a-" + token
	spanB := "e2efx-span-b-" + token
	child := "e2efx-child-" + token
	pathA := "/e2e/fx-a-" + token
	pathB := "/e2e/fx-b-" + token
	routeA := "GET " + pathA
	routeB := "GET " + pathB

	insertTraceFixture(t, []traceFixtureRow{
		{
			Count:              20,
			ServiceName:        svcA,
			SpanName:           spanA,
			ResourceAttributes: map[string]string{"k8s.namespace.name": nsA},
			SpanAttributes:     map[string]string{"http.method": "GET", "http.target": pathA + "?token=1"},
		},
		{
			Count:              8,
			ServiceName:        svcB,
			SpanName:           spanB,
			ResourceAttributes: map[string]string{"k8s.namespace.name": nsB},
			SpanAttributes:     map[string]string{"http.method": "GET", "http.route": pathB, "http.target": pathB + "?foo=1"},
		},
		{
			Count:              5,
			ServiceName:        svcA,
			SpanName:           child,
			ParentSpanId:       "deadbeefdeadbeef",
			ResourceAttributes: map[string]string{"k8s.namespace.name": nsA},
			SpanAttributes:     map[string]string{"http.method": "GET", "http.target": pathA},
		},
	})
	t.Cleanup(func() { deleteTraceFixture(t, svcA, svcB) })

	projectID := defaultProjectID(t)
	base := map[string]any{
		"view":        "traces",
		"include_aux": true,
	}

	a := facetPop{ns: nsA, svc: svcA, route: routeA, span: spanA, n: 20}
	b := facetPop{ns: nsB, svc: svcB, route: routeB, span: spanB, n: 8}

	var traces overviewTraces
	waitUntil(t, 15*time.Second, "independent trace facet fixture", func() bool {
		traces = fetchOverviewTraces(t, projectID, base)
		return traces.Error == "" &&
			facetValueCount(traces.Facets, "ApiRoute", routeA) == 20 &&
			facetValueCount(traces.Facets, "Namespace", nsA) == 20 &&
			facetValueCount(traces.Facets, "SpanName", spanA) == 20
	})
	if traces.Error != "" {
		t.Fatalf("overview traces error: %s", traces.Error)
	}

	t.Run("baseline", func(t *testing.T) {
		assertTracePopulations(t, traces, a, b, "Namespace", "ServiceName", "ApiRoute", "SpanName")
		assertTraceFacet(t, traces, "SpanName", child, 0)
	})

	t.Run("ApiRoute=A", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"field": "ApiRoute", "op": "=", "value": routeA})
		got := fetchOverviewTraces(t, projectID, q)
		assertTracePopulations(t, got, a, b, "ApiRoute")
		assertTraceListHasNoService(t, got, svcB)
	})

	t.Run("ServiceName=A", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"field": "ServiceName", "op": "=", "value": svcA})
		got := fetchOverviewTraces(t, projectID, q)
		assertTracePopulations(t, got, a, b, "ServiceName")
		assertTraceListHasNoService(t, got, svcB)
	})

	t.Run("SpanName=A", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"field": "SpanName", "op": "=", "value": spanA})
		got := fetchOverviewTraces(t, projectID, q)
		assertTracePopulations(t, got, a, b, "SpanName")
		assertTraceListHasNoService(t, got, svcB)
	})

	t.Run("Namespace=A", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"field": "Namespace", "op": "=", "value": nsA})
		got := fetchOverviewTraces(t, projectID, q)
		assertTracePopulations(t, got, a, b, "Namespace")
		assertTraceListHasNoService(t, got, svcB)
	})

	t.Run("ApiRoute=A and ServiceName=A", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"field": "ApiRoute", "op": "=", "value": routeA},
			map[string]string{"field": "ServiceName", "op": "=", "value": svcA},
		)
		got := fetchOverviewTraces(t, projectID, q)
		// Each skipped field still applies the other filter, so B drops everywhere.
		assertTracePopulations(t, got, a, b)
		assertTraceListHasNoService(t, got, svcB)
	})

	t.Run("ApiRoute!=A", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"field": "ApiRoute", "op": "!=", "value": routeA})
		got := fetchOverviewTraces(t, projectID, q)
		assertTracePopulations(t, got, b, a, "ApiRoute")
		assertTraceListHasNoService(t, got, svcA)
	})
}

type facetPop struct {
	ns, svc, route, span string
	n                    uint64
}

func (p facetPop) value(field string) string {
	switch field {
	case "Namespace":
		return p.ns
	case "ServiceName":
		return p.svc
	case "ApiRoute":
		return p.route
	case "SpanName":
		return p.span
	default:
		return ""
	}
}

func assertTracePopulations(t *testing.T, traces overviewTraces, keep, drop facetPop, skipSelf ...string) {
	t.Helper()
	if traces.Error != "" {
		t.Fatalf("overview traces error: %s", traces.Error)
	}
	skip := map[string]bool{}
	for _, field := range skipSelf {
		skip[field] = true
	}
	for _, field := range []string{"Namespace", "ServiceName", "ApiRoute", "SpanName"} {
		assertTraceFacet(t, traces, field, keep.value(field), keep.n)
		wantDrop := uint64(0)
		if skip[field] {
			wantDrop = drop.n
		}
		assertTraceFacet(t, traces, field, drop.value(field), wantDrop)
	}
}

func assertTraceListHasNoService(t *testing.T, traces overviewTraces, svc string) {
	t.Helper()
	for _, s := range traces.Traces {
		if s.Service == svc {
			t.Fatalf("trace list still has service %s: %+v", svc, s)
		}
	}
}

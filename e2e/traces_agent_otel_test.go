//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// The demo traffic calls nextjs-demo and express-demo, which are instrumented
// with OpenTelemetry, so the node agent (eBPF, the client side) and the SDK
// (the server side) both record each request. The two spans of a request must
// be found by the same filters and agree on whether it failed: the agent sets
// the Kubernetes namespace and the target an instrumented server sets, and
// marks a call failed on 5xx only, as the SDK of a server and Coroot's SLIs do.
func TestAgentAndOtelSpansDescribeARequestAlike(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	projectID := defaultProjectID(t)

	for _, request := range []struct {
		apiRoute string
		failed   bool
	}{
		{"GET /products/42", false}, // 404
		{"GET /api/error", true},    // 500
	} {
		t.Run(request.apiRoute, func(t *testing.T) {
			query := map[string]any{"view": "traces", "include_aux": true, "filters": []map[string]string{
				{"field": "ApiRoute", "op": "=", "value": request.apiRoute},
				{"field": "Namespace", "op": "=", "value": "coroot-dev"},
			}}
			var traces overviewTraces
			waitUntil(t, 90*time.Second, "agent and OpenTelemetry spans of "+request.apiRoute, func() bool {
				traces = fetchOverviewTraces(t, projectID, query)
				return traces.Error == "" &&
					facetValueCount(traces.Facets, "Source", "agent") > 0 &&
					facetValueCount(traces.Facets, "Source", "otel") > 0
			})
			if traces.Error != "" {
				t.Fatalf("overview traces error: %s", traces.Error)
			}
			sources := map[bool]int{}
			for _, span := range traces.Traces {
				agent := strings.HasPrefix(span.Service, "/")
				sources[agent]++
				if span.Status.Error != request.failed {
					t.Fatalf("%s span of %s (agent: %v) has error %v, want %v", request.apiRoute, span.Service, agent, span.Status.Error, request.failed)
				}
			}
			if sources[true] == 0 || sources[false] == 0 {
				t.Fatalf("the list has %d agent and %d OpenTelemetry spans, want both", sources[true], sources[false])
			}
		})
	}
}

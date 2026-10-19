//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// chSpanRows reads recent root spans of the dev ClickHouse, one TSV line each.
func chSpanRows(t *testing.T, columns, condition string) [][]string {
	t.Helper()
	out, err := chDo(chHTTPBase(), chLogsDatabase(t),
		"SELECT "+columns+" FROM otel_traces_distributed WHERE Timestamp > now() - INTERVAL 3 MINUTE AND ParentSpanId = '' AND "+condition+" FORMAT TSV", nil)
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			rows = append(rows, strings.Split(line, "\t"))
		}
	}
	return rows
}

// The demo traffic calls nextjs-demo and express-demo, which are instrumented
// with OpenTelemetry, so the node agent (eBPF, at the client) and the SDK (at
// the server) both record each request. The two spans must agree on the
// request: the same Kubernetes namespace and the same failure (5xx only, as
// the SDK of a server and Coroot's SLIs count it). The agent's span is the
// second record, so the Traces page lists the SDK's once per request.
func TestAgentAndOtelSpansDescribeARequestAlike(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	projectID := defaultProjectID(t)

	for _, request := range []struct {
		apiRoute string
		server   string
		failed   bool
	}{
		{"GET /products/42", "coroot-dev/nextjs-demo", false}, // 404
		{"GET /api/error", "coroot-dev/express-demo", true},   // 500
	} {
		t.Run(request.apiRoute, func(t *testing.T) {
			condition := "ApiRoute = '" + request.apiRoute + "' AND Server = '" + request.server + "'"
			var rows [][]string
			waitUntil(t, 90*time.Second, "agent and OpenTelemetry spans of "+request.apiRoute, func() bool {
				rows = chSpanRows(t, "startsWith(ServiceName, '/') AS agent, StatusCode, Counted, splitByChar('/', Server)[1]", condition)
				agent, otel := 0, 0
				for _, row := range rows {
					if row[0] == "1" {
						agent++
					} else {
						otel++
					}
				}
				return agent > 0 && otel > 0
			})
			for _, row := range rows {
				agent, failed, counted, namespace := row[0] == "1", row[1] == "STATUS_CODE_ERROR", row[2] == "1", row[3]
				if failed != request.failed {
					t.Fatalf("a span (agent: %v) has error %v, want %v", agent, failed, request.failed)
				}
				if namespace != "coroot-dev" {
					t.Fatalf("a span (agent: %v) is in namespace %q", agent, namespace)
				}
				if counted == agent {
					t.Fatalf("a span (agent: %v) has Counted %v: the SDK's span counts the request, the agent's is its second record", agent, counted)
				}
			}

			query := map[string]any{"view": "traces", "include_aux": true, "filters": []map[string]string{
				{"field": "ApiRoute", "op": "=", "value": request.apiRoute},
				{"field": "Server", "op": "=", "value": request.server},
			}}
			// the collector reads which servers record their side from
			// trace_server_sources, so a restart of Coroot counts no request twice
			traces := fetchOverviewTraces(t, projectID, query)
			if traces.Error != "" {
				t.Fatalf("overview traces error: %s", traces.Error)
			}
			if len(traces.Traces) == 0 {
				t.Fatalf("no spans listed")
			}
			for _, span := range traces.Traces {
				if strings.HasPrefix(span.Service, "/") {
					t.Fatalf("the list has the agent's second record of a request (%s)", span.Service)
				}
				if span.Status.Error != request.failed {
					t.Fatalf("a span of %s has error %v, want %v", span.Service, span.Status.Error, request.failed)
				}
			}
		})
	}
}

// The node agent records a request at the client and the SDK at the server,
// under different service names; Server, which the collector fills from the
// world, puts both under the application that served it. Server =
// coroot-dev/express-demo finds the 500s of /api/error whichever side recorded
// them, and Client tells who sent them.
func TestServerJoinsAgentAndOtelSpans(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	projectID := defaultProjectID(t)
	const express = "coroot-dev/express-demo"
	const demoTraffic = "coroot-dev/demo-traffic"

	var services map[string]int
	waitUntil(t, 120*time.Second, "agent and OpenTelemetry spans served by express-demo", func() bool {
		services = map[string]int{}
		for _, row := range chSpanRows(t, "ServiceName, Client", "Server = '"+express+"' AND ApiRoute = 'GET /api/error'") {
			if row[1] == demoTraffic {
				services[row[0]]++
			}
		}
		return services["express-demo"] > 0 && services["/k8s/coroot-dev/demo-traffic"] > 0
	})

	query := map[string]any{"view": "traces", "include_aux": true, "filters": []map[string]string{
		{"field": "Server", "op": "=", "value": express},
		{"field": "ApiRoute", "op": "=", "value": "GET /api/error"},
	}}
	traces := fetchOverviewTraces(t, projectID, query)
	if traces.Error != "" {
		t.Fatalf("overview traces error: %s", traces.Error)
	}
	if len(traces.Traces) == 0 {
		t.Fatalf("no spans listed")
	}
	for _, span := range traces.Traces {
		if !span.Status.Error {
			t.Fatalf("a span of /api/error of %s is not an error", span.Service)
		}
	}
	if n := facetValueCount(traces.Facets, "Client", demoTraffic); n == 0 {
		t.Fatalf("no requests from demo-traffic in the Client facet: %v", traces.Facets)
	}
	if got := chartTotal(traces.RequestsChart, "5xx"); got == 0 {
		t.Fatalf("no 5xx in the requests chart")
	}
}

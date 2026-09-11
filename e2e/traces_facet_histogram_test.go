//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Extra rows in otel_traces_histogram must not change sidebar facet counts.
// Those counts always come from otel_traces, so a no-op Namespace filter
// cannot switch Application/SpanName off the MV and jump.
func TestOverviewTraceFacetsIgnoreHistogramInflation(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	ns := "e2ehisto-ns-" + token
	svc := "e2ehisto-svc-" + token
	span := "e2ehisto-span-" + token
	path := "/e2e/histo-" + token
	route := "GET " + path

	insertTraceFixture(t, []traceFixtureRow{{
		Count:              20,
		ServiceName:        svc,
		SpanName:           span,
		ResourceAttributes: map[string]string{"k8s.namespace.name": ns},
		SpanAttributes:     map[string]string{"http.method": "GET", "http.target": path},
	}})
	insertTraceHistogramInflation(t, svc, span, 1000)
	t.Cleanup(func() { deleteTraceFixture(t, svc) })

	projectID := defaultProjectID(t)
	base := map[string]any{
		"view":        "traces",
		"include_aux": true,
	}

	var traces overviewTraces
	waitUntil(t, 15*time.Second, "trace fixture visible for histogram inflation", func() bool {
		traces = fetchOverviewTraces(t, projectID, base)
		return traces.Error == "" && facetValueCount(traces.Facets, "ApiRoute", route) == 20
	})

	assertTraceFacet(t, traces, "ServiceName", svc, 20)
	assertTraceFacet(t, traces, "SpanName", span, 20)
	assertTraceFacet(t, traces, "Namespace", ns, 20)

	withNS := queryWithFilters(base, map[string]string{"field": "Namespace", "op": "=", "value": ns})
	filtered := fetchOverviewTraces(t, projectID, withNS)
	assertTraceFacet(t, filtered, "ServiceName", svc, 20)
	assertTraceFacet(t, filtered, "SpanName", span, 20)
	assertTraceFacet(t, filtered, "Namespace", ns, 20)
	assertTraceFacet(t, filtered, "ApiRoute", route, 20)
}

func insertTraceHistogramInflation(t *testing.T, service, span string, extra uint64) {
	t.Helper()
	ts := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Minute).Format("2006-01-02 15:04:05")
	line, err := json.Marshal(map[string]any{
		"ServiceName":     service,
		"SpanName":        span,
		"SpanKind":        "SPAN_KIND_SERVER",
		"Root":            1,
		"NetSockPeerAddr": "",
		"NetPeerName":     "",
		"NetPeerPort":     "",
		"Timestamp":       ts,
		"Bucket":          5.0,
		"Total":           extra,
		"Failed":          0,
	})
	if err != nil {
		t.Fatal(err)
	}
	chExec(t, `INSERT INTO otel_traces_histogram (
		ServiceName, SpanName, SpanKind, Root, NetSockPeerAddr, NetPeerName, NetPeerPort,
		Timestamp, Bucket, Total, Failed
	) FORMAT JSONEachRow`, append(line, '\n'))
}

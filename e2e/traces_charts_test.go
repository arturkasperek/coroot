//go:build e2e

package e2e

import (
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"
)

// One service whose root spans have every status class, with durations that
// put p50 and p95 in known duration buckets. Checks what the Traces page shows:
// the requests chart by status class, the latency chart and quantiles, the
// Status facet, the Status filter, and the selection of the requests slower
// than p95.
func TestOverviewTraceStatusCharts(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	service := "e2echarts-svc-" + token
	spanOK := "e2echarts-ok-" + token
	spanNotFound := "e2echarts-not-found-" + token
	spanUnavailable := "e2echarts-unavailable-" + token
	spanFailedQuery := "e2echarts-failed-query-" + token
	spanInternal := "e2echarts-internal-" + token
	const millisecond = int64(time.Millisecond)

	insertTraceFixture(t, []traceFixtureRow{
		{Count: 20, ServiceName: service, SpanName: spanOK, DurationNs: 10 * millisecond,
			SpanAttributes: map[string]string{"http.status_code": "200"}},
		{Count: 5, ServiceName: service, SpanName: spanNotFound, DurationNs: 30 * millisecond,
			SpanAttributes: map[string]string{"http.response.status_code": "404"}}, // the newer convention
		{Count: 3, ServiceName: service, SpanName: spanUnavailable, DurationNs: 600 * millisecond, StatusCode: "STATUS_CODE_ERROR",
			SpanAttributes: map[string]string{"http.status_code": "503"}},
		{Count: 2, ServiceName: service, SpanName: spanFailedQuery, DurationNs: 2000 * millisecond, StatusCode: "STATUS_CODE_ERROR"},
		{Count: 4, ServiceName: service, SpanName: spanInternal, DurationNs: 2 * millisecond},
	})
	t.Cleanup(func() { deleteTraceFixture(t, service) })

	projectID := defaultProjectID(t)
	base := queryWithFilters(map[string]any{"view": "traces", "include_aux": true},
		map[string]string{"field": "ServiceName", "op": "=", "value": service})

	var traces overviewTraces
	waitUntil(t, 30*time.Second, "trace chart fixture", func() bool {
		traces = fetchOverviewTraces(t, projectID, base)
		return traces.Error == "" && chartTotal(traces.RequestsChart, "2xx") == 20 && chartTotal(traces.RequestsChart, "other") == 4 &&
			facetValueCount(traces.Facets, "Status", "other") == 4
	})
	if traces.Error != "" {
		t.Fatalf("overview traces error: %s", traces.Error)
	}

	t.Run("requests chart counts each status class, in the order of the chart", func(t *testing.T) {
		assertChartTotals(t, traces.RequestsChart, []string{"2xx", "4xx", "5xx", "error", "other"},
			map[string]float64{"2xx": 20, "4xx": 5, "5xx": 3, "error": 2, "other": 4})
	})

	t.Run("colors are left to the frontend", func(t *testing.T) {
		for _, chart := range []*overviewChart{traces.RequestsChart, traces.LatencyChart} {
			for _, series := range chart.Series {
				if series.Color != "" {
					t.Fatalf("series %s has color %q", series.Name, series.Color)
				}
			}
		}
	})

	// 34 requests: 4 of 2 ms, 20 of 10 ms, 5 of 30 ms, 3 of 600 ms, 2 of 2 s.
	// p50 (the 17th) is the 13th of the 20 in the 10..25 ms bucket; p95 (the
	// 32.3rd) is 0.3 of the 2 in the 1..2.5 s bucket.
	expectedP50 := 0.010 + 0.015*13/20
	expectedP95 := 1 + 1.5*0.3/2
	t.Run("latency quantiles of the whole view", func(t *testing.T) {
		if traces.LatencyQuantiles == nil {
			t.Fatal("no latency quantiles")
		}
		assertSeconds(t, "p50", traces.LatencyQuantiles.P50, expectedP50)
		assertSeconds(t, "p95", traces.LatencyQuantiles.P95, expectedP95)
	})

	t.Run("latency chart has p95 behind p50", func(t *testing.T) {
		if got := seriesNames(traces.LatencyChart); fmt.Sprint(got) != "[p95 p50]" {
			t.Fatalf("latency series %v, want [p95 p50]", got)
		}
		// every request is in the same minute, so the one point is the whole view
		assertSeconds(t, "p50 point", chartMax(traces.LatencyChart, "p50"), expectedP50)
		assertSeconds(t, "p95 point", chartMax(traces.LatencyChart, "p95"), expectedP95)
	})

	t.Run("Status facet lists the classes in the order of the chart", func(t *testing.T) {
		assertFacetValues(t, traces.Facets, "Status", []string{"2xx", "4xx", "5xx", "error", "other"},
			map[string]uint64{"2xx": 20, "4xx": 5, "5xx": 3, "error": 2, "other": 4})
	})

	t.Run("Status filter narrows the list, the charts and the other facets", func(t *testing.T) {
		got := fetchOverviewTraces(t, projectID, queryWithFilters(base, map[string]string{"field": "Status", "op": "=", "value": "4xx"}))
		assertTraceListSpanNames(t, got, map[string]int{spanNotFound: 5})
		assertChartTotals(t, got.RequestsChart, []string{"4xx"}, map[string]float64{"4xx": 5})
		assertSeconds(t, "p50 of 4xx", got.LatencyQuantiles.P50, 0.025+0.025*2.5/5)
		if n := facetValueCount(got.Facets, "SpanName", spanOK); n != 0 {
			t.Fatalf("SpanName facet still counts %s: %d", spanOK, n)
		}
		// the filtered group keeps its counts, so another class can be picked
		assertFacetValues(t, got.Facets, "Status", []string{"2xx", "4xx", "5xx", "error", "other"},
			map[string]uint64{"2xx": 20, "4xx": 5, "5xx": 3, "error": 2, "other": 4})
	})

	t.Run("values of one field add up", func(t *testing.T) {
		got := fetchOverviewTraces(t, projectID, queryWithFilters(base,
			map[string]string{"field": "Status", "op": "=", "value": "4xx"},
			map[string]string{"field": "Status", "op": "=", "value": "5xx"}))
		assertTraceListSpanNames(t, got, map[string]int{spanNotFound: 5, spanUnavailable: 3})
		assertChartTotals(t, got.RequestsChart, []string{"4xx", "5xx"}, map[string]float64{"4xx": 5, "5xx": 3})

		// a second application, which has no spans, takes nothing away
		got = fetchOverviewTraces(t, projectID, queryWithFilters(base,
			map[string]string{"field": "ServiceName", "op": "=", "value": "e2echarts-absent-" + token}))
		if n := len(got.Traces); n != 34 {
			t.Fatalf("trace list has %d spans, want 34", n)
		}
		assertChartTotals(t, got.RequestsChart, []string{"2xx", "4xx", "5xx", "error", "other"},
			map[string]float64{"2xx": 20, "4xx": 5, "5xx": 3, "error": 2, "other": 4})
	})

	t.Run("Status exclusion", func(t *testing.T) {
		got := fetchOverviewTraces(t, projectID, queryWithFilters(base, map[string]string{"field": "Status", "op": "!=", "value": "other"}))
		assertChartTotals(t, got.RequestsChart, []string{"2xx", "4xx", "5xx", "error"},
			map[string]float64{"2xx": 20, "4xx": 5, "5xx": 3, "error": 2})
		if n := len(got.Traces); n != 30 {
			t.Fatalf("trace list has %d spans, want 30", n)
		}
	})

	t.Run("requests slower than p95 are selected, the charts stay whole", func(t *testing.T) {
		query := copyQuery(base)
		query["dur_from"] = strconv.FormatFloat(math.Round(traces.LatencyQuantiles.P95*1000)/1000, 'f', -1, 64)
		got := fetchOverviewTraces(t, projectID, query)
		assertTraceListSpanNames(t, got, map[string]int{spanFailedQuery: 2})
		assertChartTotals(t, got.RequestsChart, []string{"2xx", "4xx", "5xx", "error", "other"},
			map[string]float64{"2xx": 20, "4xx": 5, "5xx": 3, "error": 2, "other": 4})
	})
}

func seriesNames(chart *overviewChart) []string {
	var names []string
	if chart != nil {
		for _, series := range chart.Series {
			names = append(names, series.Name)
		}
	}
	return names
}

// chartTotal sums the points of a series; 0 when the chart or the series is missing.
func chartTotal(chart *overviewChart, name string) float64 {
	if chart == nil {
		return 0
	}
	var total float64
	for _, series := range chart.Series {
		if series.Name != name {
			continue
		}
		for _, value := range series.Data {
			if value != nil {
				total += *value
			}
		}
	}
	return total
}

func chartMax(chart *overviewChart, name string) float64 {
	maximum := math.NaN()
	for _, series := range chart.Series {
		if series.Name != name {
			continue
		}
		for _, value := range series.Data {
			if value != nil && (math.IsNaN(maximum) || *value > maximum) {
				maximum = *value
			}
		}
	}
	return maximum
}

func assertChartTotals(t *testing.T, chart *overviewChart, names []string, totals map[string]float64) {
	t.Helper()
	if chart == nil {
		t.Fatal("no chart")
	}
	if got := seriesNames(chart); fmt.Sprint(got) != fmt.Sprint(names) {
		t.Fatalf("series %v, want %v", got, names)
	}
	for name, want := range totals {
		if got := chartTotal(chart, name); got != want {
			t.Fatalf("series %s total %v, want %v", name, got, want)
		}
	}
}

func assertSeconds(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.0005 {
		t.Fatalf("%s = %vs, want %vs", what, got, want)
	}
}

func assertFacetValues(t *testing.T, groups apiFacetGroups, key string, values []string, counts map[string]uint64) {
	t.Helper()
	for _, group := range groups {
		if group.Key != key {
			continue
		}
		var got []string
		for _, value := range group.Values {
			got = append(got, value.Value)
		}
		if fmt.Sprint(got) != fmt.Sprint(values) {
			t.Fatalf("facet %s values %v, want %v", key, got, values)
		}
		for value, want := range counts {
			if n := facetValueCount(groups, key, value); n != want {
				t.Fatalf("facet %s=%s count %d, want %d", key, value, n, want)
			}
		}
		return
	}
	t.Fatalf("no facet %s in %v", key, groups)
}

func assertTraceListSpanNames(t *testing.T, traces overviewTraces, want map[string]int) {
	t.Helper()
	got := map[string]int{}
	for _, span := range traces.Traces {
		got[span.Name]++
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("trace list spans %v, want %v", got, want)
	}
}

// The requests chart and the Status facet of the Traces page count the same
// window: over a view in the past whose ends are not whole minutes (nor whole
// steps), the bars add up to the facet's values.
func TestTracesChartAndFacetsCountTheSameRequests(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	projectID := defaultProjectID(t)

	// a short view, so that a cluster started a quarter of an hour ago has its data
	traces := fetchOverviewTracesBetween(t, projectID, map[string]any{"view": "traces"}, "now-12m", "now-3m")
	if traces.Error != "" {
		t.Fatalf("overview traces error: %s", traces.Error)
	}
	var chart float64
	if traces.RequestsChart != nil {
		for _, series := range traces.RequestsChart.Series {
			chart += chartTotal(traces.RequestsChart, series.Name)
		}
	}
	var facet uint64
	for _, group := range traces.Facets {
		if group.Key == "Status" {
			for _, value := range group.Values {
				facet += value.Count
			}
		}
	}
	if chart == 0 {
		t.Fatalf("no requests in the chart")
	}
	if uint64(chart) != facet {
		t.Fatalf("the chart counts %v requests, the Status facet %d", chart, facet)
	}
}

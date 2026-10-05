//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The log histogram and the facet counts are answered from the per-minute
// rollup (otel_logs_rollup) when the query allows it, and from the raw table
// otherwise. These tests go through the HTTP API and check that
//   - both paths give the same numbers, and the numbers are the right ones;
//   - the rollup is used when it should be, and only then (system.query_log).

type logsRangeView struct {
	Error string `json:"error"`
	Chart struct {
		Ctx struct {
			Step int64 `json:"step"` // ms
		} `json:"ctx"`
		Series []struct {
			Name string     `json:"name"`
			Data []*float64 `json:"data"`
		} `json:"series"`
	} `json:"chart"`
	Facets apiFacetGroups `json:"facets"`
}

func fetchLogsRange(t *testing.T, projectID string, query map[string]any, from, to time.Time) logsRangeView {
	t.Helper()
	q, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	u := fmt.Sprintf("%s/api/project/%s/overview/logs?from=%d&to=%d&query=%s",
		corootBase(), url.PathEscape(projectID), from.UnixMilli(), to.UnixMilli(), url.QueryEscape(string(q)))
	var env apiEnvelope
	httpGetJSON(t, u, &env)
	var ov struct {
		Logs logsRangeView `json:"logs"`
	}
	if err := json.Unmarshal(env.Data, &ov); err != nil {
		t.Fatalf("decode overview logs: %v\n%s", err, env.Data)
	}
	if ov.Logs.Error != "" {
		t.Fatalf("overview logs error: %s", ov.Logs.Error)
	}
	return ov.Logs
}

// chartCounts: severity name -> points of the histogram, -1 where the API sends null
func (v logsRangeView) chartCounts() map[string][]float64 {
	out := map[string][]float64{}
	for _, s := range v.Chart.Series {
		pts := make([]float64, len(s.Data))
		for i, p := range s.Data {
			if p == nil {
				pts[i] = -1
			} else {
				pts[i] = *p
			}
		}
		out[s.Name] = pts
	}
	return out
}

func (v logsRangeView) chartTotals() map[string]float64 {
	out := map[string]float64{}
	for name, pts := range v.chartCounts() {
		for _, p := range pts {
			if p > 0 {
				out[name] += p
			}
		}
	}
	return out
}

// facetCounts: "facet|value" -> count
func (v logsRangeView) facetCounts() map[string]uint64 {
	out := map[string]uint64{}
	for _, g := range v.Facets {
		for _, val := range g.Values {
			out[g.Key+"|"+val.Value] = val.Count
		}
	}
	return out
}

// chQueryOne runs a query that returns one value, on the ClickHouse the dev
// Coroot uses.
func chQueryOne(t *testing.T, query string) string {
	t.Helper()
	out, err := chDo(chHTTPBase(), "", query, nil)
	if err != nil {
		t.Fatalf("clickhouse: %v\n%s", err, query)
	}
	return strings.TrimSpace(string(out))
}

// chNowMicros is the ClickHouse clock, so that the comparison with
// system.query_log does not depend on this machine's clock.
func chNowMicros(t *testing.T) int64 {
	t.Helper()
	n, err := strconv.ParseInt(chQueryOne(t, "SELECT toUnixTimestamp64Micro(now64(6))"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// rollupUse counts the queries that mention the namespace since a moment (the
// token is in the query text) and read the rollup, split into the histogram
// and the facet counts.
func rollupUse(t *testing.T, ns string, sinceMicros int64) (histogram, facets, raw int) {
	t.Helper()
	chQueryOne(t, "SYSTEM FLUSH LOGS ON CLUSTER coroot")
	q := fmt.Sprintf(`SELECT
	  countIf(position(query, 'otel_logs_rollup') > 0 AND position(query, 'toStartOfInterval(Minute') > 0),
	  countIf(position(query, 'otel_logs_rollup') > 0 AND position(query, 'toStartOfInterval(Minute') = 0),
	  countIf(position(query, 'otel_logs_rollup') = 0 AND position(query, 'otel_logs') > 0)
	FROM clusterAllReplicas(coroot, system.query_log)
	WHERE type = 'QueryFinish' AND is_initial_query AND event_time_microseconds >= fromUnixTimestamp64Micro(%d)
	  AND position(query, '%s') > 0 AND position(query, 'system.query_log') = 0
	FORMAT TSV`, sinceMicros, ns)
	f := strings.Fields(chQueryOne(t, q))
	if len(f) != 3 {
		t.Fatalf("unexpected query_log answer %v", f)
	}
	var n [3]int
	for i := range n {
		n[i], _ = strconv.Atoi(f[i])
	}
	return n[0], n[1], n[2]
}

type rollupFixtureTotals struct {
	bySeverity map[string]float64
	byApp      map[string]uint64
	byHost     map[string]uint64
	total      uint64
}

// Rows spread over 40 minutes, two applications, three severities, and the
// host name on the resource, like the agent sends it.
func insertRollupFixture(t *testing.T, token, ns string, first time.Time) rollupFixtureTotals {
	t.Helper()
	tot := rollupFixtureTotals{bySeverity: map[string]float64{}, byApp: map[string]uint64{}, byHost: map[string]uint64{}}
	api := "/k8s/" + ns + "/api"
	worker := "/k8s/" + ns + "/worker"
	var rows []logFixtureRow
	add := func(n int, sevText string, sevNum int, svc, host, app, sev string, ts time.Time) {
		if n == 0 {
			return
		}
		rows = append(rows, logFixtureRow{
			Count: n, SeverityText: sevText, SeverityNumber: sevNum, ServiceName: svc, Host: host,
			Body: "e2e rollup fixture", Timestamp: ts.UTC(),
		})
		tot.bySeverity[sev] += float64(n)
		tot.byApp[app] += uint64(n)
		tot.byHost[host] += uint64(n)
		tot.total += uint64(n)
	}
	for i := 0; i < 40; i++ {
		ts := first.Add(time.Duration(i)*time.Minute + 7*time.Second)
		add(3+i%3, "INFO", 9, api, "node-a", "api", "info", ts)
		add(1+i%2, "ERROR", 17, api, "node-a", "api", "error", ts.Add(11*time.Second))
		add(2, "WARN", 13, worker, "node-b", "worker", "warning", ts.Add(23*time.Second))
		if i%5 == 0 {
			add(1, "INFO", 9, worker, "node-b", "worker", "info", ts.Add(31*time.Second))
		}
	}
	insertLogFixture(t, token, rows)
	return tot
}

func TestOverviewLogRollup(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	stamp := time.Now().UnixMilli()
	token := fmt.Sprintf("e2e-rollup-%d", stamp)
	ns := fmt.Sprintf("e2e-ru-%d", stamp)

	// a window in the past, so that it is complete, and on whole minutes
	to := time.Now().UTC().Add(-25 * time.Minute).Truncate(time.Minute)
	from := to.Add(-90 * time.Minute)
	// A DELETE on otel_logs does not reach the rollup (the materialized view
	// only sees inserts), so the fixture of this and of earlier, failed runs
	// is removed from both tables.
	purgeRollupFixtures(t)
	want := insertRollupFixture(t, token, ns, to.Add(-75*time.Minute))
	t.Cleanup(func() { purgeRollupFixtures(t) })

	projectID := defaultProjectID(t)
	byNamespace := map[string]string{"name": "Namespace", "op": "=", "value": ns}
	// matches every row, but names an attribute the rollup does not keep, so
	// the raw table answers
	rawOnly := map[string]string{"name": "no.such.attribute", "op": "!=", "value": "zzz"}
	query := func(filters ...map[string]string) map[string]any {
		return map[string]any{"view": "messages", "limit": 5, "filters": filters}
	}

	// the data is visible through the API before anything is compared
	var base logsRangeView
	waitUntil(t, 30*time.Second, "fixture logs in the histogram", func() bool {
		base = fetchLogsRange(t, projectID, query(byNamespace, rawOnly), from, to)
		return base.chartTotals()["info"] == want.bySeverity["info"]
	})

	assertTotals := func(t *testing.T, v logsRangeView) {
		t.Helper()
		got := v.chartTotals()
		for _, sev := range []string{"info", "warning", "error"} {
			if got[sev] != want.bySeverity[sev] {
				t.Errorf("histogram %s=%v want %v (%v)", sev, got[sev], want.bySeverity[sev], got)
			}
		}
		f := v.facetCounts()
		for app, n := range want.byApp {
			if f["Application|"+app] != n {
				t.Errorf("facet Application=%s: %d want %d", app, f["Application|"+app], n)
			}
		}
		for host, n := range want.byHost {
			if f["host.name|"+host] != n {
				t.Errorf("facet host.name=%s: %d want %d", host, f["host.name|"+host], n)
			}
		}
		for sev, n := range want.bySeverity {
			if f["Severity|"+sev] != uint64(n) {
				t.Errorf("facet Severity=%s: %d want %d", sev, f["Severity|"+sev], uint64(n))
			}
		}
		if f["Cluster|"+clusterName(f)] != want.total {
			t.Errorf("facet Cluster total want %d, facets %v", want.total, f)
		}
	}

	equal := func(t *testing.T, a, b logsRangeView) {
		t.Helper()
		ac, bc := a.chartCounts(), b.chartCounts()
		if len(ac) != len(bc) {
			t.Fatalf("histograms have different series: %v vs %v", ac, bc)
		}
		for name, pts := range ac {
			other := bc[name]
			if len(pts) != len(other) {
				t.Fatalf("series %s: %d points vs %d", name, len(pts), len(other))
			}
			for i := range pts {
				if pts[i] != other[i] {
					t.Errorf("series %s point %d: %v vs %v", name, i, pts[i], other[i])
				}
			}
		}
		// The Namespace facet ignores the Namespace filter, so it lists every
		// namespace of the cluster: live logs and the fixtures of other tests,
		// which come and go (and a deleted fixture stays in the rollup). Only
		// this test's namespace is compared.
		own := func(m map[string]uint64) map[string]uint64 {
			out := map[string]uint64{}
			for k, n := range m {
				if strings.HasPrefix(k, "Namespace|") && k != "Namespace|"+ns {
					continue
				}
				out[k] = n
			}
			return out
		}
		af, bf := own(a.facetCounts()), own(b.facetCounts())
		for k, n := range af {
			if bf[k] != n {
				t.Errorf("facet %s: %d vs %d", k, n, bf[k])
			}
		}
		for k, n := range bf {
			if af[k] != n {
				t.Errorf("facet %s: %d vs %d", k, af[k], n)
			}
		}
	}

	t.Run("whole minutes: the rollup is used and agrees with the raw table", func(t *testing.T) {
		since := chNowMicros(t)
		fast := fetchLogsRange(t, projectID, query(byNamespace), from, to)
		if fast.Chart.Ctx.Step != 60000 {
			t.Fatalf("step=%dms, the rollup needs whole minutes", fast.Chart.Ctx.Step)
		}
		hist, facets, _ := rollupUse(t, ns, since)
		if hist != 1 || facets == 0 {
			t.Fatalf("the rollup should answer the histogram and the facets: histogram=%d facets=%d", hist, facets)
		}
		raw := fetchLogsRange(t, projectID, query(byNamespace, rawOnly), from, to)
		assertTotals(t, fast)
		assertTotals(t, raw)
		equal(t, fast, raw)
	})

	t.Run("a window that cuts minutes is answered from the raw table", func(t *testing.T) {
		since := chNowMicros(t)
		cut := fetchLogsRange(t, projectID, query(byNamespace), from.Add(17*time.Second), to.Add(-13*time.Second))
		hist, facets, raw := rollupUse(t, ns, since)
		if hist != 0 || facets != 0 || raw == 0 {
			t.Fatalf("expected the raw table only: rollup histogram=%d facets=%d, raw=%d", hist, facets, raw)
		}
		assertTotals(t, cut)
	})

	t.Run("filters the rollup cannot serve go to the raw table", func(t *testing.T) {
		for name, filters := range map[string][]map[string]string{
			"message":   {byNamespace, {"name": "Message", "op": "contains", "value": "fixture"}},
			"attribute": {byNamespace, {"name": "e2e.facets", "op": "=", "value": token}},
			"other":     {byNamespace, rawOnly},
		} {
			t.Run(name, func(t *testing.T) {
				since := chNowMicros(t)
				v := fetchLogsRange(t, projectID, query(filters...), from, to)
				hist, facets, _ := rollupUse(t, ns, since)
				if hist != 0 || facets != 0 {
					t.Fatalf("the rollup answered a query it cannot serve: histogram=%d facets=%d", hist, facets)
				}
				assertTotals(t, v)
			})
		}
		t.Run("trace id", func(t *testing.T) {
			since := chNowMicros(t)
			v := fetchLogsRange(t, projectID, query(byNamespace, map[string]string{"name": "TraceId", "op": "=", "value": "00000000000000000000000000000001"}), from, to)
			hist, facets, _ := rollupUse(t, ns, since)
			if hist != 0 || facets != 0 {
				t.Fatalf("the rollup answered a trace id query: histogram=%d facets=%d", hist, facets)
			}
			if n := v.chartTotals()["info"]; n != 0 {
				t.Fatalf("a trace id that no log has matched %v logs", n)
			}
		})
	})

	t.Run("filters the rollup keeps give the right numbers", func(t *testing.T) {
		for name, tc := range map[string]struct {
			filter map[string]string
			sev    string
			info   float64
		}{
			"application": {map[string]string{"name": "Application", "op": "=", "value": "worker"}, "warning", want.bySeverity["warning"]},
			"host":        {map[string]string{"name": "host.name", "op": "=", "value": "node-a"}, "error", want.bySeverity["error"]},
			"severity":    {map[string]string{"name": "Severity", "op": "=", "value": "error"}, "error", want.bySeverity["error"]},
		} {
			t.Run(name, func(t *testing.T) {
				since := chNowMicros(t)
				fast := fetchLogsRange(t, projectID, query(byNamespace, tc.filter), from, to)
				if hist, _, _ := rollupUse(t, ns, since); hist != 1 {
					t.Fatalf("the rollup should answer this histogram, got %d", hist)
				}
				raw := fetchLogsRange(t, projectID, query(byNamespace, tc.filter, rawOnly), from, to)
				if got := fast.chartTotals()[tc.sev]; got != tc.info {
					t.Errorf("%s=%v want %v", tc.sev, got, tc.info)
				}
				equal(t, fast, raw)
			})
		}
	})
}

// the Cluster facet is named after the project's cluster
func clusterName(f map[string]uint64) string {
	for k := range f {
		if strings.HasPrefix(k, "Cluster|") {
			return strings.TrimPrefix(k, "Cluster|")
		}
	}
	return ""
}

func purgeRollupFixtures(t *testing.T) {
	t.Helper()
	chExec(t, "DELETE FROM otel_logs ON CLUSTER coroot WHERE startsWith(LogAttributes['e2e.facets'], 'e2e-rollup-')", nil)
	chExec(t, "DELETE FROM otel_logs_rollup ON CLUSTER coroot WHERE startsWith(Namespace, 'e2e-ru-')", nil)
}

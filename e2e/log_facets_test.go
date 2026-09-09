//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestOverviewLogFacetCounts(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("e2e-facets-%d", time.Now().UnixNano())
	insertFacetFixture(t, token)
	t.Cleanup(func() { deleteFacetFixture(t, token) })

	projectID := defaultProjectID(t)
	base := map[string]any{
		"view":  "messages",
		"limit": 100,
		"filters": []map[string]string{{
			"name": "e2e.facets", "op": "=", "value": token,
		}},
	}

	var logs overviewLogs
	waitUntil(t, 15*time.Second, "overview log facet counts for fixture", func() bool {
		logs = fetchOverviewLogs(t, projectID, base)
		return logs.Error == "" && clusterTotal(logs) == 50
	})
	if logs.Error != "" {
		t.Fatalf("overview logs error: %s", logs.Error)
	}
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/express-demo", 40)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/nextjs-demo", 10)
	assertFacet(t, logs, "host.name", "node-a", 40)
	assertFacet(t, logs, "host.name", "node-b", 10)
	assertFacet(t, logs, "Severity", "info", 38)
	assertFacet(t, logs, "Severity", "error", 12)
	assertFacet(t, logs, "Severity", "warning", 0)
	assertFacet(t, logs, "Severity", "unknown", 0)
	if clusterTotal(logs) != 50 {
		t.Fatalf("Cluster total=%d want 50 facets=%v", clusterTotal(logs), logs.Facets)
	}

	withSev := copyQuery(base)
	withSev["filters"] = append(filtersOf(base), map[string]string{"name": "Severity", "op": "=", "value": "error"})
	logs = fetchOverviewLogs(t, projectID, withSev)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/express-demo", 10)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/nextjs-demo", 2)
	assertFacet(t, logs, "host.name", "node-a", 10)
	assertFacet(t, logs, "host.name", "node-b", 2)
	assertFacet(t, logs, "Severity", "info", 38)
	assertFacet(t, logs, "Severity", "error", 12)

	withApp := copyQuery(base)
	withApp["filters"] = append(filtersOf(base), map[string]string{
		"name": "service.name", "op": "=", "value": "/k8s/coroot-dev/express-demo",
	})
	logs = fetchOverviewLogs(t, projectID, withApp)
	assertFacet(t, logs, "Severity", "info", 30)
	assertFacet(t, logs, "Severity", "error", 10)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/express-demo", 40)
	assertFacet(t, logs, "service.name", "/k8s/coroot-dev/nextjs-demo", 10)

	express := "/k8s/coroot-dev/express-demo"
	nextjs := "/k8s/coroot-dev/nextjs-demo"

	t.Run("Severity=error and service.name=express", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Severity", "op": "=", "value": "error"},
			map[string]string{"name": "service.name", "op": "=", "value": express},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertFacet(t, logs, "host.name", "node-a", 10)
		assertFacet(t, logs, "host.name", "node-b", 0)
		assertClusterTotal(t, logs, 10)
		assertFacet(t, logs, "Severity", "info", 30)
		assertFacet(t, logs, "Severity", "error", 10)
		assertFacet(t, logs, "service.name", express, 10)
		assertFacet(t, logs, "service.name", nextjs, 2)
	})

	t.Run("Severity=error and host.name=node-a", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Severity", "op": "=", "value": "error"},
			map[string]string{"name": "host.name", "op": "=", "value": "node-a"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertFacet(t, logs, "service.name", express, 10)
		assertFacet(t, logs, "service.name", nextjs, 0)
		assertFacet(t, logs, "host.name", "node-a", 10)
		assertFacet(t, logs, "host.name", "node-b", 2)
		assertFacet(t, logs, "Severity", "info", 30)
		assertFacet(t, logs, "Severity", "error", 10)
		assertClusterTotal(t, logs, 10)
	})

	t.Run("service.name=express and host.name=node-a", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "service.name", "op": "=", "value": express},
			map[string]string{"name": "host.name", "op": "=", "value": "node-a"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertFacet(t, logs, "Severity", "info", 30)
		assertFacet(t, logs, "Severity", "error", 10)
		assertFacet(t, logs, "service.name", express, 40)
		assertFacet(t, logs, "service.name", nextjs, 0)
		assertFacet(t, logs, "host.name", "node-a", 40)
		assertFacet(t, logs, "host.name", "node-b", 0)
		assertClusterTotal(t, logs, 40)
	})

	t.Run("Severity=info and service.name=nextjs", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Severity", "op": "=", "value": "info"},
			map[string]string{"name": "service.name", "op": "=", "value": nextjs},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertFacet(t, logs, "host.name", "node-a", 0)
		assertFacet(t, logs, "host.name", "node-b", 8)
		assertClusterTotal(t, logs, 8)
		assertFacet(t, logs, "Severity", "info", 8)
		assertFacet(t, logs, "Severity", "error", 2)
		assertFacet(t, logs, "service.name", express, 30)
		assertFacet(t, logs, "service.name", nextjs, 8)
	})
}

func queryWithFilters(base map[string]any, extra ...map[string]string) map[string]any {
	q := copyQuery(base)
	q["filters"] = append(filtersOf(base), extra...)
	return q
}

func assertFacet(t *testing.T, logs overviewLogs, key, value string, want uint64) {
	t.Helper()
	got := facetCount(logs, key, value)
	if got != want {
		t.Fatalf("facet %s=%s count=%d want %d facets=%v", key, value, got, want, logs.Facets)
	}
}

func assertClusterTotal(t *testing.T, logs overviewLogs, want uint64) {
	t.Helper()
	if got := clusterTotal(logs); got != want {
		t.Fatalf("Cluster total=%d want %d facets=%v", got, want, logs.Facets)
	}
}

func clusterTotal(logs overviewLogs) uint64 {
	var n uint64
	for _, g := range logs.Facets {
		if g.Key != "Cluster" {
			continue
		}
		for _, v := range g.Values {
			n += v.Count
		}
	}
	return n
}

func copyQuery(base map[string]any) map[string]any {
	b, _ := json.Marshal(base)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func filtersOf(q map[string]any) []map[string]string {
	raw, _ := json.Marshal(q["filters"])
	var filters []map[string]string
	_ = json.Unmarshal(raw, &filters)
	return filters
}

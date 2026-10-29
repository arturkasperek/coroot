//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// The Logs page of a live view shows the logs up to now: they are in
// ClickHouse within seconds, while the world of the view ends at the last
// evaluated step, a minute or so ago.
func TestOverviewLogsShowTheLastSeconds(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("e2e-live-%d", time.Now().UnixNano())
	insertLogFixture(t, token, []logFixtureRow{
		{Count: 3, SeverityText: "ERROR", SeverityNumber: 17, ServiceName: "/k8s/coroot-dev/express-demo", Host: "live-node", Body: "e2e live log",
			Timestamp: time.Now().UTC().Add(-5 * time.Second)},
	})
	t.Cleanup(func() { deleteFacetFixture(t, token) })

	projectID := defaultProjectID(t)
	query := map[string]any{
		"view":    "messages",
		"limit":   100,
		"filters": []map[string]string{{"name": "e2e.facets", "op": "=", "value": token}},
	}

	var logs overviewLogs
	waitUntil(t, 10*time.Second, "the logs of 5 seconds ago on the Logs page", func() bool {
		logs = fetchOverviewLogs(t, projectID, query)
		return logs.Error == "" && len(logs.Entries) == 3
	})
	if total := chartTotal(logs.Chart, "error"); total != 3 {
		t.Fatalf("histogram total: got %v, want 3", total)
	}
}

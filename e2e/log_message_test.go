//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	msgFailCount    = 8
	msgTimeoutCount = 5
	msgOKCount      = 4
	msgAllCount     = msgFailCount + msgTimeoutCount + msgOKCount
)

func TestOverviewLogMessageSubstring(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("e2e-msg-%d", time.Now().UnixNano())
	insertLogFixture(t, token, []logFixtureRow{
		{Count: msgFailCount, SeverityText: "ERROR", SeverityNumber: 17, ServiceName: "/k8s/coroot-dev/express-demo", Host: "msg-node-a", Body: "express simulated failure"},
		{Count: msgTimeoutCount, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s/coroot-dev/express-demo", Host: "msg-node-a", Body: "request timeout"},
		{Count: msgOKCount, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s/coroot-dev/express-demo", Host: "msg-node-a", Body: "health check ok"},
	})
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
	waitUntil(t, 15*time.Second, "overview logs for message substring fixture", func() bool {
		logs = fetchOverviewLogs(t, projectID, base)
		return logs.Error == "" && clusterTotal(logs) == uint64(msgAllCount)
	})
	if logs.Error != "" {
		t.Fatalf("overview logs error: %s", logs.Error)
	}

	t.Run("Message contains fail matches failure substring", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Message", "op": "contains", "value": "fail"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(msgFailCount))
		assertAllEntriesContain(t, logs, "failure")
	})

	t.Run("Message contains FAIL is case-insensitive", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Message", "op": "contains", "value": "FAIL"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(msgFailCount))
		assertAllEntriesContain(t, logs, "failure")
	})

	t.Run("Message contains timeout matches whole word", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Message", "op": "contains", "value": "timeout"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(msgTimeoutCount))
		assertAllEntriesContain(t, logs, "timeout")
	})

	t.Run("Message contains fail timeout ANDs tokens", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Message", "op": "contains", "value": "fail timeout"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, 0)
		if len(logs.Entries) != 0 {
			t.Fatalf("entries=%d want 0", len(logs.Entries))
		}
	})

	t.Run("Message not contains fail excludes failure rows", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Message", "op": "not contains", "value": "fail"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(msgTimeoutCount+msgOKCount))
		for _, e := range logs.Entries {
			if strings.Contains(strings.ToLower(e.Message), "fail") {
				t.Fatalf("unexpected failure row: %q", e.Message)
			}
		}
	})
}

func assertAllEntriesContain(t *testing.T, logs overviewLogs, substr string) {
	t.Helper()
	if len(logs.Entries) == 0 {
		t.Fatalf("no entries, want messages containing %q", substr)
	}
	for _, e := range logs.Entries {
		if !strings.Contains(strings.ToLower(e.Message), strings.ToLower(substr)) {
			t.Fatalf("entry %q does not contain %q", e.Message, substr)
		}
	}
}

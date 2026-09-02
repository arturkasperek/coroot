//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestCorootLinksOtelLogsToTraces(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	httpGetOK(t, expressBase()+"/health")

	projectID := defaultProjectID(t)
	appID := expressAppID(t, projectID)

	waitUntil(t, 90*time.Second, "OTEL logs linked to traces via TraceId", func() bool {
		httpGetOK(t, expressBase()+"/api/hello")
		logs := fetchAppLogs(t, projectID, appID, "otel", "express hello")
		t.Logf("logs status=%s source=%s entries=%d", logs.Status, logs.Source, len(logs.Entries))
		if logs.Status != "ok" || logs.Source != "otel" {
			return false
		}

		var traceID string
		for _, e := range logs.Entries {
			if !strings.Contains(e.Message, "express hello") || e.TraceId == "" {
				continue
			}
			traceID = e.TraceId
			break
		}
		if traceID == "" {
			t.Log("no OTEL log with trace_id yet")
			return false
		}

		tr := fetchAppTrace(t, projectID, appID, traceID)
		t.Logf("trace %s status=%s spans=%d", traceID, tr.Status, len(tr.Spans))
		if tr.Status != "ok" {
			return false
		}
		hasHello := false
		for _, s := range tr.Spans {
			if s.TraceId == traceID && strings.Contains(s.Name, "/api/hello") {
				hasHello = true
				break
			}
		}
		if !hasHello {
			return false
		}

		linked := fetchAppLogsByTraceID(t, projectID, appID, traceID)
		for _, e := range linked.Entries {
			if strings.Contains(e.Message, "express hello") && e.TraceId == traceID {
				return true
			}
		}
		t.Logf("logs by TraceId entries=%d", len(linked.Entries))
		return false
	})
}

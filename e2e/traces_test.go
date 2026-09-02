//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestCorootIngestsOtelTraces(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	httpGetOK(t, expressBase()+"/health")

	projectID := defaultProjectID(t)
	appID := expressAppID(t, projectID)

	waitUntil(t, 90*time.Second, "OTEL traces for GET /api/hello", func() bool {
		httpGetOK(t, expressBase()+"/api/hello")
		tr := fetchAppTraces(t, projectID, appID)
		t.Logf("tracing status=%s spans=%d", tr.Status, len(tr.Spans))
		if tr.Status != "ok" {
			return false
		}
		for _, s := range tr.Spans {
			if strings.Contains(s.Name, "/api/hello") {
				return true
			}
		}
		return false
	})
}

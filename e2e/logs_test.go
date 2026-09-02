//go:build e2e

package e2e

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCorootIngestsContainerLogs(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	httpGetOK(t, expressBase()+"/health")

	projectID := defaultProjectID(t)
	appID := expressAppID(t, projectID)

	waitUntil(t, 90*time.Second, "container logs from Coroot API (source=agent)", func() bool {
		httpGetOK(t, expressBase()+"/api/hello")
		logs := fetchAppLogs(t, projectID, appID, "agent", "express hello")
		t.Logf("status=%s source=%s message=%s entries=%d", logs.Status, logs.Source, logs.Message, len(logs.Entries))
		if logs.Status != "ok" || logs.Source != "agent" {
			return false
		}
		if !strings.Contains(strings.ToLower(logs.Message), "container") {
			return false
		}
		for _, e := range logs.Entries {
			if !strings.Contains(e.Message, "express hello") {
				continue
			}
			svc := e.Attributes["service.name"]
			if strings.HasPrefix(svc, "/k8s/"+namespace+"/") {
				return true
			}
		}
		return false
	})
}

func TestCorootDoesNotIngestAppOtelLogs(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	httpGetOK(t, expressBase()+"/health")

	projectID := defaultProjectID(t)
	appID := expressAppID(t, projectID)
	token := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	hello := expressBase() + "/api/hello?token=" + url.QueryEscape(token)

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		httpGetOK(t, hello)
		logs := fetchAppLogs(t, projectID, appID, "otel", token)
		t.Logf("otel status=%s source=%s entries=%d", logs.Status, logs.Source, len(logs.Entries))
		for _, e := range logs.Entries {
			if !strings.Contains(e.Message, token) {
				continue
			}
			if e.Attributes["telemetry.sdk.name"] == "opentelemetry" || e.Attributes["service.name"] == "express-demo" {
				t.Fatalf("app OTEL log ingested for %q: attrs=%v", token, e.Attributes)
			}
		}
		time.Sleep(2 * time.Second)
	}
}

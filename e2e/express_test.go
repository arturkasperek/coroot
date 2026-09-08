//go:build e2e

package e2e

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

const expressHelloMetric = "express_demo_hello_total"

func TestNodeAgentDiscoversPodStartedAfterAgent(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	agentUID := nodeAgentPodUID(t)
	pod := restartExpressDemo(t)
	if got := nodeAgentPodUID(t); got != agentUID {
		t.Fatalf("node-agent pod changed during express restart (uid %s -> %s); cannot test live discovery", agentUID, got)
	}
	t.Logf("express-demo pod after agent was already running: %s", pod)

	projectID := defaultProjectID(t)
	appID := expressAppID(t, projectID)
	token := fmt.Sprintf("e2e-after-agent-%d", time.Now().UnixNano())
	hello := expressBase() + "/api/hello?token=" + url.QueryEscape(token)
	metricQuery := fmt.Sprintf(`%s{namespace="%s",pod="%s"}`, expressHelloMetric, namespace, pod)

	waitUntil(t, 90*time.Second, "logs and custom Prometheus metric for pod started after the agent", func() bool {
		httpGetOK(t, hello)
		logs := fetchAppLogs(t, projectID, appID, "agent", token)
		points := fetchPanelChartPoints(t, projectID, metricQuery)
		t.Logf("logs status=%s source=%s entries=%d metric=%s points=%d",
			logs.Status, logs.Source, len(logs.Entries), metricQuery, points)
		return agentLogsHaveMessage(logs, token) && points > 0
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

func agentLogsHaveMessage(logs logsView, marker string) bool {
	if logs.Status != "ok" || logs.Source != "agent" {
		return false
	}
	for _, e := range logs.Entries {
		if !strings.Contains(e.Message, marker) {
			continue
		}
		if strings.HasPrefix(e.Attributes["service.name"], "/k8s/"+namespace+"/") {
			return true
		}
	}
	return false
}

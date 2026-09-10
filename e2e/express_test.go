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

func TestMicroserviceChainTraceAndLogs(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	httpGetOK(t, nextjsBase()+"/")
	httpGetOK(t, expressBase()+"/health")
	httpGetOK(t, flaskBase()+"/health")

	projectID := defaultProjectID(t)
	token := fmt.Sprintf("e2e-chain-%d", time.Now().UnixNano())
	chain := nextjsBase() + "/chain?token=" + url.QueryEscape(token)

	var traceId string
	waitUntil(t, 90*time.Second, "agent logs for nextjs→express→flask chain", func() bool {
		httpGetOK(t, chain)
		logs := fetchOverviewLogs(t, projectID, map[string]any{
			"view":  "messages",
			"limit": 100,
			"filters": []map[string]string{{
				"name":  "Message",
				"op":    "contains",
				"value": token,
			}},
		})
		t.Logf("chain logs error=%q entries=%d", logs.Error, len(logs.Entries))
		for _, e := range logs.Entries {
			if e.TraceId != "" && strings.Contains(e.Message, token) {
				traceId = e.TraceId
				return true
			}
		}
		return false
	})

	waitUntil(t, 60*time.Second, "Coroot logs+trace for chain request "+traceId, func() bool {
		logs := fetchOverviewLogs(t, projectID, map[string]any{
			"view":  "messages",
			"limit": 100,
			"filters": []map[string]string{{
				"name":  "TraceId",
				"op":    "=",
				"value": traceId,
			}},
		})
		tr := fetchOverviewTraces(t, projectID, map[string]any{
			"view":     "traces",
			"trace_id": traceId,
		})
		t.Logf("traceId=%s logs=%d spans=%d error=%q/%q",
			traceId, len(logs.Entries), len(tr.Trace), logs.Error, tr.Error)
		return chainLogsForTrace(logs, token, traceId) && chainTraceLinked(tr.Trace, traceId)
	})
}

func chainLogsForTrace(logs overviewLogs, token, traceId string) bool {
	if logs.Error != "" {
		return false
	}
	var next, express, flask bool
	for _, e := range logs.Entries {
		if e.TraceId != traceId || !strings.Contains(e.Message, token) {
			continue
		}
		switch {
		case strings.Contains(e.Message, "nextjs chain"):
			next = true
		case strings.Contains(e.Message, "express chain"):
			express = true
		case strings.Contains(e.Message, "flask hello"):
			flask = true
		}
	}
	return next && express && flask
}

func chainTraceLinked(spans []overviewTraceSpan, traceId string) bool {
	byID := make(map[string]overviewTraceSpan, len(spans))
	var nextjs, express, flask []overviewTraceSpan
	for _, s := range spans {
		if s.TraceId != "" && s.TraceId != traceId {
			continue
		}
		byID[s.Id] = s
		switch s.Service {
		case "nextjs-demo":
			nextjs = append(nextjs, s)
		case "express-demo":
			express = append(express, s)
		case "flask-demo":
			flask = append(flask, s)
		}
	}
	if len(nextjs) == 0 || len(express) == 0 || len(flask) == 0 {
		return false
	}
	expressFromNext := false
	for _, s := range express {
		if p, ok := byID[s.ParentId]; ok && p.Service == "nextjs-demo" {
			expressFromNext = true
			break
		}
	}
	flaskFromExpress := false
	for _, s := range flask {
		if p, ok := byID[s.ParentId]; ok && p.Service == "express-demo" {
			flaskFromExpress = true
			break
		}
	}
	return expressFromNext && flaskFromExpress
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

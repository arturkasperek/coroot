//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	sourceAgentSvc  = "/k8s/coroot-dev/express-demo"
	sourceOtelSvc   = "express-demo"
	sourceAgentHost = "src-node-agent"
	sourceOtelHost  = "src-node-otel"

	sourceAgentInfo  = 20
	sourceAgentError = 6
	sourceOtelInfo   = 9
	sourceOtelError  = 3
)

func TestOverviewLogSourceFilters(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("e2e-source-%d", time.Now().UnixNano())
	insertLogFixture(t, token, []logFixtureRow{
		{sourceAgentInfo, "INFO", 9, sourceAgentSvc, sourceAgentHost, "e2e source agent info"},
		{sourceAgentError, "ERROR", 17, sourceAgentSvc, sourceAgentHost, "e2e source agent error"},
		{sourceOtelInfo, "INFO", 9, sourceOtelSvc, sourceOtelHost, "e2e source otel info"},
		{sourceOtelError, "ERROR", 17, sourceOtelSvc, sourceOtelHost, "e2e source otel error"},
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
	agentTotal := sourceAgentInfo + sourceAgentError
	otelTotal := sourceOtelInfo + sourceOtelError
	allTotal := agentTotal + otelTotal

	var logs overviewLogs
	waitUntil(t, 15*time.Second, "overview logs for agent+otel fixture", func() bool {
		logs = fetchOverviewLogs(t, projectID, base)
		return logs.Error == "" && clusterTotal(logs) == uint64(allTotal)
	})
	if logs.Error != "" {
		t.Fatalf("overview logs error: %s", logs.Error)
	}

	t.Run("no Source filter returns both sources", func(t *testing.T) {
		logs := fetchOverviewLogs(t, projectID, base)
		assertFacet(t, logs, "Source", "agent", uint64(agentTotal))
		assertFacet(t, logs, "Source", "otel", uint64(otelTotal))
		assertFacet(t, logs, "Severity", "info", uint64(sourceAgentInfo+sourceOtelInfo))
		assertFacet(t, logs, "Severity", "error", uint64(sourceAgentError+sourceOtelError))
		assertFacet(t, logs, "service.name", sourceAgentSvc, uint64(agentTotal))
		assertFacet(t, logs, "service.name", sourceOtelSvc, uint64(otelTotal))
		assertClusterTotal(t, logs, uint64(allTotal))
		assertEntrySources(t, logs, agentTotal, otelTotal)
	})

	t.Run("Source=agent keeps only container logs", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Source", "op": "=", "value": "agent"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(agentTotal))
		assertFacet(t, logs, "Source", "agent", uint64(agentTotal))
		assertFacet(t, logs, "Source", "otel", uint64(otelTotal))
		assertFacet(t, logs, "Severity", "info", uint64(sourceAgentInfo))
		assertFacet(t, logs, "Severity", "error", uint64(sourceAgentError))
		assertFacet(t, logs, "service.name", sourceAgentSvc, uint64(agentTotal))
		assertFacet(t, logs, "service.name", sourceOtelSvc, 0)
		assertFacet(t, logs, "host.name", sourceAgentHost, uint64(agentTotal))
		assertFacet(t, logs, "host.name", sourceOtelHost, 0)
		assertEntrySources(t, logs, agentTotal, 0)
	})

	t.Run("Source=otel keeps only OpenTelemetry logs", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Source", "op": "=", "value": "otel"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(otelTotal))
		assertFacet(t, logs, "Source", "agent", uint64(agentTotal))
		assertFacet(t, logs, "Source", "otel", uint64(otelTotal))
		assertFacet(t, logs, "Severity", "info", uint64(sourceOtelInfo))
		assertFacet(t, logs, "Severity", "error", uint64(sourceOtelError))
		assertFacet(t, logs, "service.name", sourceOtelSvc, uint64(otelTotal))
		assertFacet(t, logs, "service.name", sourceAgentSvc, 0)
		assertFacet(t, logs, "host.name", sourceOtelHost, uint64(otelTotal))
		assertFacet(t, logs, "host.name", sourceAgentHost, 0)
		assertEntrySources(t, logs, 0, otelTotal)
	})

	t.Run("Source!=agent is OpenTelemetry only", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Source", "op": "!=", "value": "agent"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(otelTotal))
		assertEntrySources(t, logs, 0, otelTotal)
		assertFacet(t, logs, "Severity", "info", uint64(sourceOtelInfo))
		assertFacet(t, logs, "Severity", "error", uint64(sourceOtelError))
	})

	t.Run("Source=agent and Source=otel ORs both sources", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Source", "op": "=", "value": "agent"},
			map[string]string{"name": "Source", "op": "=", "value": "otel"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(allTotal))
		assertEntrySources(t, logs, agentTotal, otelTotal)
	})

	t.Run("Source=agent and Severity=error", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Source", "op": "=", "value": "agent"},
			map[string]string{"name": "Severity", "op": "=", "value": "error"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(sourceAgentError))
		assertFacet(t, logs, "Source", "agent", uint64(sourceAgentError))
		assertFacet(t, logs, "Source", "otel", uint64(sourceOtelError))
		assertFacet(t, logs, "Severity", "info", uint64(sourceAgentInfo))
		assertFacet(t, logs, "Severity", "error", uint64(sourceAgentError))
		assertFacet(t, logs, "service.name", sourceAgentSvc, uint64(sourceAgentError))
		assertFacet(t, logs, "host.name", sourceAgentHost, uint64(sourceAgentError))
		assertEntrySources(t, logs, sourceAgentError, 0)
		assertEntrySeverity(t, logs, "error")
	})

	t.Run("Source=otel and Severity=info", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Source", "op": "=", "value": "otel"},
			map[string]string{"name": "Severity", "op": "=", "value": "info"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(sourceOtelInfo))
		assertFacet(t, logs, "Source", "agent", uint64(sourceAgentInfo))
		assertFacet(t, logs, "Source", "otel", uint64(sourceOtelInfo))
		assertFacet(t, logs, "Severity", "info", uint64(sourceOtelInfo))
		assertFacet(t, logs, "Severity", "error", uint64(sourceOtelError))
		assertEntrySources(t, logs, 0, sourceOtelInfo)
		assertEntrySeverity(t, logs, "info")
	})

	t.Run("Source=otel and container service.name is empty", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Source", "op": "=", "value": "otel"},
			map[string]string{"name": "service.name", "op": "=", "value": sourceAgentSvc},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, 0)
		assertEntrySources(t, logs, 0, 0)
	})

	t.Run("Source=agent and otel service.name is empty", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Source", "op": "=", "value": "agent"},
			map[string]string{"name": "service.name", "op": "=", "value": sourceOtelSvc},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, 0)
		assertEntrySources(t, logs, 0, 0)
	})

	t.Run("Source=otel and host.name of container logs is empty", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Source", "op": "=", "value": "otel"},
			map[string]string{"name": "host.name", "op": "=", "value": sourceAgentHost},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, 0)
		assertEntrySources(t, logs, 0, 0)
	})
}

func assertEntrySources(t *testing.T, logs overviewLogs, wantAgent, wantOtel int) {
	t.Helper()
	var agent, otel int
	for _, e := range logs.Entries {
		if strings.HasPrefix(e.Attributes["service.name"], "/") {
			agent++
			continue
		}
		otel++
	}
	if agent != wantAgent || otel != wantOtel {
		t.Fatalf("entries agent=%d otel=%d want agent=%d otel=%d", agent, otel, wantAgent, wantOtel)
	}
}

func assertEntrySeverity(t *testing.T, logs overviewLogs, want string) {
	t.Helper()
	for _, e := range logs.Entries {
		if e.Severity != want {
			t.Fatalf("entry severity=%q want %q message=%q", e.Severity, want, e.Message)
		}
	}
}

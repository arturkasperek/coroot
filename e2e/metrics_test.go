//go:build e2e

package e2e

import (
	"testing"
	"time"
)

func TestCorootIngestsContainerMetrics(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	httpGetOK(t, expressBase()+"/health")

	projectID := defaultProjectID(t)
	appID := expressAppID(t, projectID)

	waitUntil(t, 90*time.Second, "CPU and Memory reports from Coroot API", func() bool {
		httpGetOK(t, expressBase()+"/api/hello")
		view := fetchApplication(t, projectID, appID)
		cpu, okCPU := reportByName(view, "CPU")
		mem, okMem := reportByName(view, "Memory")
		cpuPoints, memPoints := 0, 0
		if okCPU {
			cpuPoints = chartPoints(cpu.Widgets)
		}
		if okMem {
			memPoints = chartPoints(mem.Widgets)
		}
		t.Logf("cpu status=%s points=%d memory status=%s points=%d", cpu.Status, cpuPoints, mem.Status, memPoints)
		return okCPU && okMem && cpu.Status == "ok" && mem.Status == "ok" && cpuPoints > 0 && memPoints > 0
	})
}

func TestCorootIngestsNodejsCustomMetrics(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")
	httpGetOK(t, expressBase()+"/health")

	projectID := defaultProjectID(t)

	waitUntil(t, 90*time.Second, "express_demo_requests_total from Coroot panel API", func() bool {
		httpGetOK(t, expressBase()+"/api/hello")
		points := fetchPanelChartPoints(t, projectID, "express_demo_requests_total")
		t.Logf("express_demo_requests_total points=%d", points)
		return points > 0
	})
}

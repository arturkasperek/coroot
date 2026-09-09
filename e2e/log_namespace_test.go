//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	nsK8sCorootExpress  = 15
	nsK8sCorootNextjs   = 7
	nsK8sCorootBackup   = 2
	nsK8sKubeCoredns    = 11
	nsK8sKubeExpress    = 4
	nsOtelExpressWithNS = 8
	nsOtelExpressNoNS   = 5
	nsOtelCheckout      = 6
	nsSystemdSSH        = 3

	nsCorootDev = nsK8sCorootExpress + nsK8sCorootNextjs + nsK8sCorootBackup + nsOtelExpressWithNS // 32
	nsKubeSys   = nsK8sKubeCoredns + nsK8sKubeExpress                                              // 15
	nsNA        = nsOtelExpressNoNS + nsOtelCheckout + nsSystemdSSH                                // 14
	nsAll       = nsCorootDev + nsKubeSys + nsNA                                                   // 61
	nsExpress   = nsK8sCorootExpress + nsK8sKubeExpress + nsOtelExpressWithNS + nsOtelExpressNoNS  // 32
	nsK8sAll    = nsK8sCorootExpress + nsK8sCorootNextjs + nsK8sCorootBackup + nsK8sKubeCoredns + nsK8sKubeExpress
)

func TestOverviewLogNamespaceFilters(t *testing.T) {
	requireDevCluster(t)
	httpGetOK(t, corootBase()+"/health")

	token := fmt.Sprintf("e2e-namespace-%d", time.Now().UnixNano())
	insertLogFixture(t, token, []logFixtureRow{
		{Count: nsK8sCorootExpress, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s/coroot-dev/express-demo", Host: "ns-node-express", Body: "e2e ns k8s express"},
		{Count: nsK8sCorootNextjs, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s/coroot-dev/nextjs-demo", Host: "ns-node-nextjs", Body: "e2e ns k8s nextjs"},
		{Count: nsK8sCorootBackup, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s-cronjob/coroot-dev/backup", Host: "ns-node-backup", Body: "e2e ns k8s backup"},
		{Count: nsK8sKubeCoredns, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s/kube-system/coredns", Host: "ns-node-coredns", Body: "e2e ns k8s coredns"},
		{Count: nsK8sKubeExpress, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/k8s/kube-system/express-demo", Host: "ns-node-ks-express", Body: "e2e ns k8s kube express"},
		{
			Count:              nsOtelExpressWithNS,
			SeverityText:       "INFO",
			SeverityNumber:     9,
			ServiceName:        "express-demo",
			Host:               "ns-node-otel-ns",
			Body:               "e2e ns otel express with ns",
			ResourceAttributes: map[string]string{"k8s.namespace.name": "coroot-dev"},
		},
		{Count: nsOtelExpressNoNS, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "express-demo", Host: "ns-node-otel", Body: "e2e ns otel express"},
		{Count: nsOtelCheckout, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "checkout", Host: "ns-node-checkout", Body: "e2e ns otel checkout"},
		{Count: nsSystemdSSH, SeverityText: "INFO", SeverityNumber: 9, ServiceName: "/system.slice/ssh.service", Host: "ns-node-ssh", Body: "e2e ns systemd ssh"},
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
	waitUntil(t, 15*time.Second, "overview logs for namespace fixture", func() bool {
		logs = fetchOverviewLogs(t, projectID, base)
		return logs.Error == "" && clusterTotal(logs) == uint64(nsAll)
	})
	if logs.Error != "" {
		t.Fatalf("overview logs error: %s", logs.Error)
	}

	t.Run("isolation only", func(t *testing.T) {
		logs := fetchOverviewLogs(t, projectID, base)
		assertClusterTotal(t, logs, uint64(nsAll))
		assertFacet(t, logs, "Namespace", "coroot-dev", uint64(nsCorootDev))
		assertFacet(t, logs, "Namespace", "kube-system", uint64(nsKubeSys))
		assertFacet(t, logs, "Namespace", "n/a", uint64(nsNA))
		assertFacet(t, logs, "Application", "express-demo", uint64(nsExpress))
		assertFacet(t, logs, "Application", "nextjs-demo", uint64(nsK8sCorootNextjs))
		assertFacet(t, logs, "Application", "backup", uint64(nsK8sCorootBackup))
		assertFacet(t, logs, "Application", "coredns", uint64(nsK8sKubeCoredns))
		assertFacet(t, logs, "Application", "checkout", uint64(nsOtelCheckout))
		assertFacet(t, logs, "Application", "/system.slice/ssh.service", uint64(nsSystemdSSH))
		assertNamespaceEntries(t, logs, nsK8sAll, nsOtelExpressWithNS, nsOtelExpressNoNS)
	})

	t.Run("Application=express-demo", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Application", "op": "=", "value": "express-demo"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(nsExpress))
		assertFacet(t, logs, "Namespace", "coroot-dev", uint64(nsK8sCorootExpress+nsOtelExpressWithNS))
		assertFacet(t, logs, "Namespace", "kube-system", uint64(nsK8sKubeExpress))
		assertFacet(t, logs, "Namespace", "n/a", uint64(nsOtelExpressNoNS))
		assertFacet(t, logs, "Application", "express-demo", uint64(nsExpress))
		assertFacet(t, logs, "Application", "nextjs-demo", uint64(nsK8sCorootNextjs))
		assertNamespaceEntries(t, logs, nsK8sCorootExpress+nsK8sKubeExpress, nsOtelExpressWithNS, nsOtelExpressNoNS)
	})

	t.Run("Namespace=coroot-dev and Application=express-demo", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Namespace", "op": "=", "value": "coroot-dev"},
			map[string]string{"name": "Application", "op": "=", "value": "express-demo"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		want := nsK8sCorootExpress + nsOtelExpressWithNS // 23
		assertClusterTotal(t, logs, uint64(want))
		assertNamespaceEntries(t, logs, nsK8sCorootExpress, nsOtelExpressWithNS, 0)
	})

	t.Run("Namespace=kube-system and Application=express-demo", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Namespace", "op": "=", "value": "kube-system"},
			map[string]string{"name": "Application", "op": "=", "value": "express-demo"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(nsK8sKubeExpress))
		assertNamespaceEntries(t, logs, nsK8sKubeExpress, 0, 0)
	})

	t.Run("Namespace=n/a and Application=express-demo", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Namespace", "op": "=", "value": "n/a"},
			map[string]string{"name": "Application", "op": "=", "value": "express-demo"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(nsOtelExpressNoNS))
		assertNamespaceEntries(t, logs, 0, 0, nsOtelExpressNoNS)
	})

	t.Run("Namespace=coroot-dev and Source=agent", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Namespace", "op": "=", "value": "coroot-dev"},
			map[string]string{"name": "Source", "op": "=", "value": "agent"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		want := nsK8sCorootExpress + nsK8sCorootNextjs + nsK8sCorootBackup // 24
		assertClusterTotal(t, logs, uint64(want))
		assertNamespaceEntries(t, logs, want, 0, 0)
	})

	t.Run("Namespace!=coroot-dev", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Namespace", "op": "!=", "value": "coroot-dev"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(nsKubeSys+nsNA))
		assertNamespaceEntries(t, logs, nsKubeSys, 0, nsOtelExpressNoNS)
	})

	t.Run("Namespace=coroot-dev self-exclusion", func(t *testing.T) {
		q := queryWithFilters(base, map[string]string{"name": "Namespace", "op": "=", "value": "coroot-dev"})
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, uint64(nsCorootDev))
		assertFacet(t, logs, "Namespace", "coroot-dev", uint64(nsCorootDev))
		assertFacet(t, logs, "Namespace", "kube-system", uint64(nsKubeSys))
		assertFacet(t, logs, "Namespace", "n/a", uint64(nsNA))
		assertNamespaceEntries(t, logs, nsK8sCorootExpress+nsK8sCorootNextjs+nsK8sCorootBackup, nsOtelExpressWithNS, 0)
	})

	t.Run("Namespace=kube-system and Application=nextjs-demo is empty", func(t *testing.T) {
		q := queryWithFilters(base,
			map[string]string{"name": "Namespace", "op": "=", "value": "kube-system"},
			map[string]string{"name": "Application", "op": "=", "value": "nextjs-demo"},
		)
		logs := fetchOverviewLogs(t, projectID, q)
		assertClusterTotal(t, logs, 0)
		assertNamespaceEntries(t, logs, 0, 0, 0)
	})
}

func assertNamespaceEntries(t *testing.T, logs overviewLogs, wantK8s, wantOtelWithNS, wantOtelNoNS int) {
	t.Helper()
	var k8s, otelNS, otelNo int
	for _, e := range logs.Entries {
		svc := e.Attributes["service.name"]
		switch {
		case strings.HasPrefix(svc, "/k8s"):
			k8s++
		case svc == "express-demo":
			if e.Attributes["k8s.namespace.name"] != "" {
				if e.Attributes["k8s.namespace.name"] != "coroot-dev" {
					t.Fatalf("otel-with-ns k8s.namespace.name=%q want coroot-dev message=%q", e.Attributes["k8s.namespace.name"], e.Message)
				}
				otelNS++
				continue
			}
			otelNo++
		}
	}
	if k8s != wantK8s || otelNS != wantOtelWithNS || otelNo != wantOtelNoNS {
		t.Fatalf("entries k8s=%d otel-with-ns=%d otel-no-ns=%d want k8s=%d otel-with-ns=%d otel-no-ns=%d",
			k8s, otelNS, otelNo, wantK8s, wantOtelWithNS, wantOtelNoNS)
	}
}

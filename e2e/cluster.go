//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	namespace = "coroot-dev"
	context   = "kind-coroot-dev"
)

func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := kubectlErr(args...)
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(out)
}

func kubectlErr(args ...string) (string, error) {
	all := append([]string{"--context", context, "-n", namespace}, args...)
	cmd := exec.Command("kubectl", all...)
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		cmd.Env = os.Environ()
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func requireDevCluster(t *testing.T) {
	t.Helper()
	out, err := kubectlErr("get", "deploy/coroot", "deploy/clickhouse", "deploy/postgres", "ds/coroot-node-agent")
	if err != nil {
		t.Fatalf("dev cluster %s not ready (run make dev): %v\n%s", context, err, out)
	}
}

func waitUntil(t *testing.T, timeout time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, desc)
}

func restartExpressDemo(t *testing.T) string {
	t.Helper()
	kubectl(t, "rollout", "status", "ds/coroot-node-agent", "--timeout=60s")
	kubectl(t, "rollout", "restart", "deploy/express-demo")
	kubectl(t, "rollout", "status", "deploy/express-demo", "--timeout=90s")
	waitUntil(t, 45*time.Second, "express-demo /health after pod restart", func() bool {
		resp, err := http.Get(expressBase() + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == http.StatusOK
	})
	pod := kubectl(t, "get", "pod", "-l", "app=express-demo", "-o", "jsonpath={.items[0].metadata.name}")
	if pod == "" {
		t.Fatal("express-demo pod name is empty after restart")
	}
	return pod
}

func nodeAgentPodUID(t *testing.T) string {
	t.Helper()
	return kubectl(t, "get", "pod", "-l", "app=coroot-node-agent", "-o", "jsonpath={.items[0].metadata.uid}")
}

func nodeAgentDetectedPodSince(t *testing.T, pod string, since time.Time) bool {
	t.Helper()
	out, err := kubectlErr("logs", "ds/coroot-node-agent", "--since-time="+since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Logf("node-agent logs: %v\n%s", err, out)
		return false
	}
	needle := fmt.Sprintf(`id="/k8s/%s/%s/express-demo"`, namespace, pod)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "detected a new container") && strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

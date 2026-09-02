//go:build e2e

package e2e

import (
	"bytes"
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

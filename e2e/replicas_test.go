//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	_ "github.com/lib/pq"
	v1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// corootReplica is one pod of the Coroot Deployment, reached on a port of its
// own: the tests talk to a given replica, not to whichever the Service picks.
type corootReplica struct {
	pod  string
	base string
}

var forwardingFrom = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)

// corootReplicas forwards a local port to every ready Coroot pod. The forwards
// end with the test.
func corootReplicas(t *testing.T) []corootReplica {
	t.Helper()
	pods := strings.Fields(kubectl(t, "get", "pods", "-l", "app=coroot", "--field-selector=status.phase=Running",
		"-o", "jsonpath={.items[*].metadata.name}"))
	var replicas []corootReplica
	for _, pod := range pods {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "kubectl", "--context", kubeContext(), "-n", namespace, "port-forward", "pod/"+pod, ":8080")
		cmd.Env = os.Environ()
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cancel()
			_ = cmd.Wait()
		})
		lines := bufio.NewScanner(stdout)
		port := ""
		for port == "" && lines.Scan() {
			if m := forwardingFrom.FindStringSubmatch(lines.Text()); m != nil {
				port = m[1]
			}
		}
		if port == "" {
			t.Fatalf("no port forward to %s", pod)
		}
		go func() { _, _ = io.Copy(io.Discard, stdout) }()
		replica := corootReplica{pod: pod, base: "http://127.0.0.1:" + port}
		waitUntil(t, 30*time.Second, "the forward to "+pod, func() bool {
			res, err := http.Get(replica.base + "/health")
			if err == nil {
				_ = res.Body.Close()
			}
			return err == nil && res.StatusCode == http.StatusOK
		})
		replicas = append(replicas, replica)
	}
	if len(replicas) < 2 {
		t.Fatalf("Coroot runs %d replicas, these tests need 2", len(replicas))
	}
	return replicas
}

// corootPrimary is the pod that holds the primary lock, from the logs.
func corootPrimary(t *testing.T, replicas []corootReplica) string {
	t.Helper()
	var primary string
	waitUntil(t, 60*time.Second, "a primary replica", func() bool {
		for _, replica := range replicas {
			logs, _ := kubectlErr("logs", "pod/"+replica.pod, "-c", "coroot", "--since=2m")
			if strings.Contains(logs, "evaluated") && strings.Contains(logs, "queries") {
				primary = replica.pod
				return true
			}
		}
		return false
	})
	return primary
}

type pageContext struct {
	Context struct {
		Status struct {
			Status  string `json:"status"`
			Metrics struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"metrics"`
		} `json:"status"`
	} `json:"context"`
	Data json.RawMessage `json:"data"`
}

// Every replica serves the same, fresh pages: the one that is not the primary
// (it evaluates nothing) reads up to when the primary stored the results.
func TestEveryReplicaServesFreshPages(t *testing.T) {
	requireDevCluster(t)
	replicas := corootReplicas(t)
	projectID := defaultProjectID(t)

	applications := map[string]int{}
	for _, replica := range replicas {
		var page pageContext
		httpGetJSON(t, replica.base+fmt.Sprintf("/api/project/%s/overview/applications?from=now-15m", projectID), &page)
		if page.Context.Status.Metrics.Status != "ok" {
			t.Fatalf("%s: metrics are %s: %s", replica.pod, page.Context.Status.Metrics.Status, page.Context.Status.Metrics.Message)
		}
		var overview applicationsOverview
		if err := json.Unmarshal(page.Data, &overview); err != nil {
			t.Fatal(err)
		}
		if len(overview.Applications) == 0 {
			t.Fatalf("%s: no applications", replica.pod)
		}
		applications[replica.pod] = len(overview.Applications)

		traces := fetchOverviewTracesAt(t, replica.base, projectID)
		if traces.Error != "" || len(traces.Traces) == 0 {
			t.Fatalf("%s: traces: error %q, %d spans", replica.pod, traces.Error, len(traces.Traces))
		}
	}
	first := applications[replicas[0].pod]
	for pod, count := range applications {
		if diff := count - first; diff > 2 || diff < -2 {
			t.Fatalf("the replicas see different worlds: %v (%s)", applications, pod)
		}
	}
}

func fetchOverviewTracesAt(t *testing.T, base, projectID string) overviewTraces {
	t.Helper()
	var env apiEnvelope
	httpGetJSON(t, base+fmt.Sprintf("/api/project/%s/overview/traces?from=now-15m&query=%%7B%%22view%%22%%3A%%22traces%%22%%7D", projectID), &env)
	var ov struct {
		Traces overviewTraces `json:"traces"`
	}
	if err := json.Unmarshal(env.Data, &ov); err != nil {
		t.Fatalf("decode overview traces: %v", err)
	}
	return ov.Traces
}

func httpDo(t *testing.T, method, url string, body any) string {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %s: %s", method, url, res.Status, data)
	}
	return strings.TrimSpace(string(data))
}

// A project created and given its ClickHouse on one replica gets the telemetry
// its agents send to the other: that replica creates the project's tables too.
func TestProjectCreatedOnOneReplicaIngestsOnTheOther(t *testing.T) {
	requireDevCluster(t)
	replicas := corootReplicas(t)
	one, other := replicas[0], replicas[1]

	suffix := strconv.FormatInt(time.Now().UnixNano()%1e9, 10)
	database := "e2e_replicas_" + suffix
	if _, err := chDo(chHTTPBase(), "default", "CREATE DATABASE "+database+" ON CLUSTER coroot", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = chDo(chHTTPBase(), "default", "DROP DATABASE IF EXISTS "+database+" ON CLUSTER coroot SYNC", nil)
	})

	projectID := httpDo(t, http.MethodPost, one.base+"/api/project/", map[string]any{"name": "e2e-replicas-" + suffix})
	t.Cleanup(func() { httpDo(t, http.MethodDelete, one.base+"/api/project/"+projectID, nil) })
	httpDo(t, http.MethodPut, one.base+"/api/project/"+projectID+"/integrations/clickhouse", map[string]any{
		"protocol": "native", "addr": "clickhouse:9000", "auth": map[string]string{"user": "default"}, "database": database,
	})
	var settings struct {
		ApiKeys []struct {
			Key string `json:"key"`
		} `json:"api_keys"`
	}
	if err := json.Unmarshal([]byte(httpDo(t, http.MethodGet, one.base+"/api/project/"+projectID, nil)), &settings); err != nil || len(settings.ApiKeys) == 0 {
		t.Fatalf("no api key of the new project: %v", err)
	}

	service := "e2e-replicas-" + suffix
	waitUntil(t, 90*time.Second, "the spans sent to the other replica in the project's ClickHouse", func() bool {
		sendSpan(t, other.base, settings.ApiKeys[0].Key, service)
		out, err := chDo(chHTTPBase(), database, "SELECT count() FROM otel_traces_distributed WHERE ServiceName = '"+service+"'", nil)
		return err == nil && strings.TrimSpace(string(out)) != "0"
	})
}

func sendSpan(t *testing.T, base, apiKey, service string) {
	t.Helper()
	now := uint64(time.Now().UnixNano())
	body, err := proto.Marshal(&v1.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{{Key: "service.name", Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: service}}}}},
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
			TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{2}, 8), Name: "GET /replicas",
			Kind: tracev1.Span_SPAN_KIND_SERVER, StartTimeUnixNano: now, EndTimeUnixNano: now + 1000,
		}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/traces", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Api-Key", apiKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
}

// mcpAccessToken is an MCP access token of the first user, signed as Coroot
// signs them (api.mcpIssueToken) with the auth secret of the dev database.
func mcpAccessToken(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("COROOT_DEV_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("COROOT_DEV_POSTGRES_DSN is not set")
	}
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	var userID int
	if err = database.QueryRow("SELECT value FROM settings WHERE name = 'auth_secret'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if err = database.QueryRow("SELECT id FROM users ORDER BY id LIMIT 1").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var secret string
	if err = json.Unmarshal([]byte(value), &secret); err != nil {
		secret = value
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"aud": "mcp:access", "sub": strconv.Itoa(userID), "client_id": "e2e",
		"iat": time.Now().Unix(), "exp": time.Now().Add(10 * time.Minute).Unix(),
	}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// mcpCall sends one JSON-RPC message of an MCP session to a replica and returns
// the session id it answered with and the result (or the error) it carried.
func mcpCall(t *testing.T, base, token, sessionID string, message map[string]any) (string, map[string]any) {
	t.Helper()
	data, _ := json.Marshal(message)
	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		t.Fatalf("%s %v: %s: %s", base, message["method"], res.Status, body)
	}
	if res.StatusCode == http.StatusAccepted || len(body) == 0 {
		return res.Header.Get("Mcp-Session-Id"), nil
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(string(body), "\n") {
			if rest, ok := strings.CutPrefix(line, "data:"); ok {
				body = []byte(strings.TrimSpace(rest))
			}
		}
	}
	var reply map[string]any
	if err = json.Unmarshal(body, &reply); err != nil {
		t.Fatalf("%s %v: %v: %s", base, message["method"], err, body)
	}
	return res.Header.Get("Mcp-Session-Id"), reply
}

// An MCP session goes on across replicas: started on one, its project selected
// on the other, used on the first again.
func TestMcpSessionGoesAcrossReplicas(t *testing.T) {
	requireDevCluster(t)
	replicas := corootReplicas(t)
	one, other := replicas[0], replicas[1]
	token := mcpAccessToken(t)
	projectID := defaultProjectID(t)

	sessionID, reply := mcpCall(t, one.base, token, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "e2e", "version": "1"}},
	})
	if sessionID == "" || reply["error"] != nil {
		t.Fatalf("initialize: session %q, %v", sessionID, reply)
	}
	mcpCall(t, other.base, token, sessionID, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

	_, reply = mcpCall(t, other.base, token, sessionID, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "select_project", "arguments": map[string]any{"project_id": projectID}},
	})
	if result, _ := reply["result"].(map[string]any); result == nil || result["isError"] == true {
		t.Fatalf("select_project on the other replica: %v", reply)
	}

	_, reply = mcpCall(t, one.base, token, sessionID, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "list_applications", "arguments": map[string]any{}},
	})
	result, _ := reply["result"].(map[string]any)
	if result == nil || result["isError"] == true {
		t.Fatalf("list_applications back on the first replica: %v", reply)
	}
	if text := fmt.Sprint(result["content"]); !strings.Contains(text, "express-demo") {
		t.Fatalf("list_applications: no express-demo in %s", text)
	}
}

// When the primary replica goes away, the other one becomes the primary and
// keeps the pages fresh.
// It deletes a Coroot pod, so it runs only when asked (COROOT_E2E_FAILOVER=1),
// not in the middle of the other tests.
func TestPrimaryFailsOver(t *testing.T) {
	if os.Getenv("COROOT_E2E_FAILOVER") != "1" {
		t.Skip("set COROOT_E2E_FAILOVER=1 to delete the primary Coroot pod")
	}
	requireDevCluster(t)
	replicas := corootReplicas(t)
	primary := corootPrimary(t, replicas)
	var survivor corootReplica
	for _, replica := range replicas {
		if replica.pod != primary {
			survivor = replica
		}
	}
	// the port Tilt forwards may lead to the pod deleted: only the survivor is asked
	var user userResponse
	httpGetJSON(t, survivor.base+"/api/user", &user)
	if len(user.Projects) == 0 {
		t.Fatal("no projects")
	}
	projectID := user.Projects[0].Id
	kubectl(t, "delete", "pod", primary, "--wait=false")
	waitUntil(t, 90*time.Second, survivor.pod+" to become the primary", func() bool {
		logs, _ := kubectlErr("logs", "pod/"+survivor.pod, "-c", "coroot", "--since=2m")
		return strings.Contains(logs, "this instance is the primary")
	})
	waitUntil(t, 60*time.Second, "fresh pages on "+survivor.pod, func() bool {
		var page pageContext
		httpGetJSON(t, survivor.base+fmt.Sprintf("/api/project/%s/overview/applications?from=now-15m", projectID), &page)
		return page.Context.Status.Metrics.Status == "ok"
	})
	kubectl(t, "rollout", "status", "deploy/coroot", "--timeout=180s")
	waitUntil(t, 60*time.Second, "Coroot back on its forwarded port", func() bool {
		res, err := http.Get(corootBase() + "/health")
		if err == nil {
			_ = res.Body.Close()
		}
		return err == nil && res.StatusCode == http.StatusOK
	})
}

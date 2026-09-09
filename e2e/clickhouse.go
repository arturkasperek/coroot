//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func chHTTPBase() string {
	if v := strings.TrimSpace(os.Getenv("COROOT_DEV_CLICKHOUSE_HTTP")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:18123"
}

func chUser() string {
	if v := strings.TrimSpace(os.Getenv("COROOT_DEV_CLICKHOUSE_USER")); v != "" {
		return v
	}
	return "default"
}

func chPassword() string {
	return os.Getenv("COROOT_DEV_CLICKHOUSE_PASSWORD")
}

var (
	chDBOnce sync.Once
	chDBName string
	chDBErr  error
)

func chLogsDatabase(t *testing.T) string {
	t.Helper()
	chDBOnce.Do(func() {
		chDBName, chDBErr = resolveLogsDatabase()
	})
	if chDBErr != nil {
		t.Fatal(chDBErr)
	}
	return chDBName
}

func resolveLogsDatabase() (string, error) {
	base := chHTTPBase()
	if err := chPing(base); err != nil {
		return "", fmt.Errorf("clickhouse %s: %v (is Tilt forwarding ClickHouse on 18123?)", base, err)
	}
	out, err := chDo(base, "", "SELECT database FROM system.tables WHERE name = 'otel_logs'", nil)
	if err != nil {
		return "", err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return pickLogsDatabase(names)
}

func pickLogsDatabase(names []string) (string, error) {
	var corootDBs []string
	hasDefault := false
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || n == "system" || strings.EqualFold(n, "INFORMATION_SCHEMA") {
			continue
		}
		if n == "default" {
			hasDefault = true
			continue
		}
		if strings.HasPrefix(n, "coroot_") && len(n) > len("coroot_") {
			corootDBs = append(corootDBs, n)
		}
	}
	if hasDefault {
		return "default", nil
	}
	if len(corootDBs) > 0 {
		sort.Strings(corootDBs)
		return corootDBs[0], nil
	}
	return "", fmt.Errorf("no otel_logs table found (is make-dev running?)")
}

func chPing(base string) error {
	resp, err := http.Get(strings.TrimRight(base, "/") + "/ping")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("clickhouse ping %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func chDo(base, database, query string, body []byte) ([]byte, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid ClickHouse HTTP URL %q", base)
	}
	q := u.Query()
	if database != "" {
		q.Set("database", database)
	}
	if query != "" {
		q.Set("query", query)
	}
	u.RawQuery = q.Encode()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), rdr)
	if err != nil {
		return nil, err
	}
	if user := chUser(); user != "" {
		req.Header.Set("X-ClickHouse-User", user)
	}
	if password := chPassword(); password != "" {
		req.Header.Set("X-ClickHouse-Key", password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf("clickhouse %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func chExec(t *testing.T, query string, body []byte) {
	t.Helper()
	db := chLogsDatabase(t)
	if _, err := chDo(chHTTPBase(), db, query, body); err != nil {
		t.Fatalf("clickhouse exec: %v", err)
	}
}

func insertFacetFixture(t *testing.T, token string) {
	t.Helper()
	// Overview clamps `to` to Prometheus cache GetTo (~45–60s behind wall clock).
	now := time.Now().UTC().Add(-2 * time.Minute)
	var buf bytes.Buffer
	type spec struct {
		n, sevNum            int
		sev, svc, host, body string
	}
	rows := []spec{
		{30, 9, "INFO", "/k8s/coroot-dev/express-demo", "node-a", "e2e express info"},
		{10, 17, "ERROR", "/k8s/coroot-dev/express-demo", "node-a", "e2e express error"},
		{8, 9, "INFO", "/k8s/coroot-dev/nextjs-demo", "node-b", "e2e nextjs info"},
		{2, 17, "ERROR", "/k8s/coroot-dev/nextjs-demo", "node-b", "e2e nextjs error"},
	}
	for _, s := range rows {
		for i := 0; i < s.n; i++ {
			line, err := json.Marshal(map[string]any{
				"Timestamp":      now.Format("2006-01-02 15:04:05.000000000"),
				"TraceId":        "",
				"SpanId":         "",
				"TraceFlags":     0,
				"SeverityText":   s.sev,
				"SeverityNumber": s.sevNum,
				"ServiceName":    s.svc,
				"Body":           s.body,
				"ResourceAttributes": map[string]string{
					"service.name": s.svc,
					"host.name":    s.host,
				},
				"LogAttributes": map[string]string{"e2e.facets": token},
			})
			if err != nil {
				t.Fatal(err)
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
	}
	chExec(t, `INSERT INTO otel_logs (
		Timestamp, TraceId, SpanId, TraceFlags,
		SeverityText, SeverityNumber, ServiceName, Body,
		ResourceAttributes, LogAttributes
	) FORMAT JSONEachRow`, buf.Bytes())
}

func deleteFacetFixture(t *testing.T, token string) {
	t.Helper()
	q := fmt.Sprintf("DELETE FROM otel_logs WHERE LogAttributes['e2e.facets'] = '%s'", token)
	chExec(t, q, nil)
}

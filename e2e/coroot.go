//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func corootBase() string {
	if v := os.Getenv("COROOT_E2E_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:18080"
}

func expressBase() string {
	if v := os.Getenv("COROOT_E2E_EXPRESS_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:13001"
}

func httpGetJSON(t *testing.T, rawURL string, dest any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d\n%s", rawURL, resp.StatusCode, body)
	}
	if dest == nil {
		return
	}
	if err := json.Unmarshal(body, dest); err != nil {
		t.Fatalf("GET %s: decode: %v\n%s", rawURL, err, body)
	}
}

func httpGetOK(t *testing.T, rawURL string) {
	t.Helper()
	resp, err := http.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", rawURL, resp.StatusCode)
	}
}

type apiEnvelope struct {
	Data json.RawMessage `json:"data"`
}

type userResponse struct {
	Projects []struct {
		Id   string `json:"id"`
		Name string `json:"name"`
	} `json:"projects"`
}

type applicationsOverview struct {
	Applications []struct {
		Id string `json:"id"`
	} `json:"applications"`
}

type logsView struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Source  string `json:"source"`
	Entries []struct {
		Message    string            `json:"message"`
		Attributes map[string]string `json:"attributes"`
		TraceId    string            `json:"trace_id"`
	} `json:"entries"`
}

type tracingView struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Spans   []struct {
		Service string `json:"service"`
		TraceId string `json:"trace_id"`
		Name    string `json:"name"`
	} `json:"spans"`
}

func defaultProjectID(t *testing.T) string {
	t.Helper()
	var user userResponse
	httpGetJSON(t, corootBase()+"/api/user", &user)
	if len(user.Projects) == 0 {
		t.Fatal("Coroot API returned no projects")
	}
	return user.Projects[0].Id
}

func expressAppID(t *testing.T, projectID string) string {
	t.Helper()
	var env apiEnvelope
	httpGetJSON(t, corootBase()+fmt.Sprintf("/api/project/%s/overview/applications?from=now-1h", projectID), &env)
	var ov applicationsOverview
	if err := json.Unmarshal(env.Data, &ov); err != nil {
		t.Fatalf("decode applications overview: %v\n%s", err, env.Data)
	}
	for _, app := range ov.Applications {
		if strings.HasSuffix(app.Id, ":express-demo") {
			return app.Id
		}
	}
	t.Fatalf("express-demo not in Coroot applications (is the demo running in Tilt?)\napps=%v", ov.Applications)
	return ""
}

type applicationView struct {
	Reports []auditReport `json:"reports"`
}

type auditReport struct {
	Name    string          `json:"name"`
	Status  string          `json:"status"`
	Widgets json.RawMessage `json:"widgets"`
}

func fetchApplication(t *testing.T, projectID, appID string) applicationView {
	t.Helper()
	u := fmt.Sprintf("%s/api/project/%s/app/%s?from=now-1h",
		corootBase(),
		url.PathEscape(projectID),
		url.PathEscape(appID),
	)
	var env apiEnvelope
	httpGetJSON(t, u, &env)
	var view applicationView
	if err := json.Unmarshal(env.Data, &view); err != nil {
		t.Fatalf("decode application: %v\n%s", err, env.Data)
	}
	return view
}

func fetchPanelChartPoints(t *testing.T, projectID, query string) int {
	t.Helper()
	cfg := map[string]any{
		"source": map[string]any{
			"metrics": map[string]any{
				"queries": []map[string]string{{
					"query":  query,
					"legend": "{{__name__}}",
				}},
			},
		},
		"widget": map[string]any{"chart": map[string]any{"display": "line"}},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u := fmt.Sprintf("%s/api/project/%s/panel/data?from=now-1h&query=%s",
		corootBase(),
		url.PathEscape(projectID),
		url.QueryEscape(string(b)),
	)
	var panel struct {
		Chart json.RawMessage `json:"chart"`
	}
	httpGetJSON(t, u, &panel)
	if len(panel.Chart) == 0 || string(panel.Chart) == "null" {
		return 0
	}
	return chartPoints(panel.Chart)
}

func reportByName(view applicationView, name string) (auditReport, bool) {
	for _, r := range view.Reports {
		if r.Name == name {
			return r, true
		}
	}
	return auditReport{}, false
}

func chartPoints(raw json.RawMessage) int {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0
	}
	return countSeriesPoints(v)
}

func countSeriesPoints(v any) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		if series, ok := x["series"].([]any); ok {
			for _, s := range series {
				sm, ok := s.(map[string]any)
				if !ok {
					continue
				}
				data, ok := sm["data"].([]any)
				if !ok {
					continue
				}
				for _, p := range data {
					if p != nil {
						n++
					}
				}
			}
		}
		for k, child := range x {
			if k == "series" {
				continue
			}
			n += countSeriesPoints(child)
		}
	case []any:
		for _, child := range x {
			n += countSeriesPoints(child)
		}
	}
	return n
}

func fetchAppLogs(t *testing.T, projectID, appID, source, marker string) logsView {
	t.Helper()
	return fetchAppLogsQuery(t, projectID, appID, map[string]any{
		"source": source,
		"view":   "messages",
		"limit":  100,
		"filters": []map[string]string{{
			"name":  "Message",
			"op":    "~",
			"value": marker,
		}},
	})
}

func fetchAppLogsByTraceID(t *testing.T, projectID, appID, traceID string) logsView {
	t.Helper()
	return fetchAppLogsQuery(t, projectID, appID, map[string]any{
		"source": "otel",
		"view":   "messages",
		"limit":  100,
		"filters": []map[string]string{{
			"name":  "TraceId",
			"op":    "=",
			"value": traceID,
		}},
	})
}

func fetchAppLogsQuery(t *testing.T, projectID, appID string, query map[string]any) logsView {
	t.Helper()
	q, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	u := fmt.Sprintf("%s/api/project/%s/app/%s/logs?from=now-15m&query=%s",
		corootBase(),
		url.PathEscape(projectID),
		url.PathEscape(appID),
		url.QueryEscape(string(q)),
	)
	var env apiEnvelope
	httpGetJSON(t, u, &env)
	var view logsView
	if err := json.Unmarshal(env.Data, &view); err != nil {
		t.Fatalf("decode logs: %v\n%s", err, env.Data)
	}
	return view
}

func fetchAppTrace(t *testing.T, projectID, appID, traceID string) tracingView {
	t.Helper()
	u := fmt.Sprintf("%s/api/project/%s/app/%s/tracing?from=now-15m&trace=%s",
		corootBase(),
		url.PathEscape(projectID),
		url.PathEscape(appID),
		url.QueryEscape("otel:"+traceID+"::"),
	)
	var env apiEnvelope
	httpGetJSON(t, u, &env)
	var view tracingView
	if err := json.Unmarshal(env.Data, &view); err != nil {
		t.Fatalf("decode tracing: %v\n%s", err, env.Data)
	}
	return view
}

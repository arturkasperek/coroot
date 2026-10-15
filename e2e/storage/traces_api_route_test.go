//go:build e2e

package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ApiRoute is computed when a span is inserted, the way the trace list shows
// the route (traceApiRoute in front/src/utils/traceColumns.js), so that the
// API Route filter finds the spans the list shows with that route.
func TestTraceApiRoute(t *testing.T) {
	cases := []struct {
		name       string
		attributes string
		apiRoute   string
	}{
		{"route of the framework", "map('http.method', 'GET', 'http.route', '/users/:id', 'http.target', '/users/7?x=1')", "GET /users/:id"},
		{"target without its query", "map('http.method', 'GET', 'http.target', '/api/chain?token=abc')", "GET /api/chain"},
		{"new convention", "map('http.request.method', 'POST', 'url.path', '/api/orders')", "POST /api/orders"},
		// the node agent (eBPF) records the whole URL of a request
		{"path of the url", "map('http.method', 'GET', 'http.url', 'http://10.42.0.117:3000/api/error?debug=1')", "GET /api/error"},
		{"url without a method", "map('http.url', 'http://express-demo:3000/api/slow')", "/api/slow"},
		{"not http", "map('db.system', 'postgresql')", ""},
	}
	e := chtest.New(t)
	var values []string
	for index, span := range cases {
		values = append(values, fmt.Sprintf("(now(), 'trace-%d', 'span-%d', '%s', 'SPAN_KIND_SERVER', 'service', %s)", index, index, span.name, span.attributes))
	}
	e.Exec(t, "INSERT INTO otel_traces (Timestamp, TraceId, SpanId, SpanName, SpanKind, ServiceName, SpanAttributes) VALUES "+strings.Join(values, ", "))

	client, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer client.Close()
	rows, err := client.Query(context.Background(), "SELECT SpanName, ApiRoute FROM @@table_otel_traces@@")
	require.NoError(t, err)
	defer rows.Close()
	apiRoutes := map[string]string{}
	for rows.Next() {
		var name, apiRoute string
		require.NoError(t, rows.Scan(&name, &apiRoute))
		apiRoutes[name] = apiRoute
	}
	for _, span := range cases {
		assert.Equal(t, span.apiRoute, apiRoutes[span.name], span.name)
	}
}

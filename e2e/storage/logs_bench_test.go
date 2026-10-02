//go:build e2e

package storage

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/require"
)

// Timings of the product's own queries on a large table. Run with
// COROOT_BENCH=1 make test-e2e; it prints, it asserts nothing.
func benchBest(t *testing.T, name string, n int, f func()) {
	t.Helper()
	var best time.Duration
	for i := 0; i < n; i++ {
		start := time.Now()
		f()
		if d := time.Since(start); i == 0 || d < best {
			best = d
		}
	}
	t.Logf("BENCH %-52s %8.1f ms", name, float64(best.Microseconds())/1000)
}

func TestBenchLogs(t *testing.T) {
	if os.Getenv("COROOT_BENCH") == "" {
		t.Skip("COROOT_BENCH is not set")
	}
	const rows = 30_000_000
	e := chtest.New(t)
	start := time.Now()
	e.Exec(t, fmt.Sprintf(`
INSERT INTO otel_logs (Timestamp, TraceId, SpanId, TraceFlags, SeverityText, SeverityNumber, ServiceName, Body, ResourceAttributes, LogAttributes)
WITH number AS n, cityHash64(n, 1) AS h1, cityHash64(n, 2) AS h2, cityHash64(n, 3) AS h3
SELECT toDateTime64('%s', 9) + toIntervalMillisecond(toInt64(n) * 100), '', '', 0, 'x',
  multiIf(h2 %% 1000 < 5, 17, h2 %% 1000 < 40, 13, h2 %% 1000 < 120, 5, 9),
  if(h1 %% 10 < 7, concat('/k8s/ns-', toString(h1 %% 12), '/app-', toString(h1 %% 300)), concat('otel-svc-', toString(h1 %% 100))),
  multiIf(
    h3 %% 20 = 0, concat('GET /api/orders/', toString(h1 %% 10000000), ' 200 in ', toString(h2 %% 900), 'ms'),
    h3 %% 20 = 1, concat('connection to db-', toString(h2 %% 8), '.internal:5432 failed: timeout after ', toString(h3 %% 30), 's, retrying'),
    h3 %% 20 = 2, concat('User ', toString(h1 %% 200000), ' logged in from 10.', toString(h2 %% 256), '.1.1'),
    h3 %% 20 = 3, concat('order ord-', toString(h1 %% 900000000), ' shipped to warehouse wh-', toString(h2 %% 30)),
    h3 %% 20 = 4, concat('Failed to send email to user', toString(h1 %% 500000), '@example.com: SMTP 451'),
    concat('worker ', toString(h1 %% 50), ' processed batch ', toString(h2 %% 100000), ' in ', toString(h3 %% 5000), 'ms')),
  map('host.name', concat('node-', toString(h1 %% 40)), 'k8s.pod.name', concat('pod-', toString(h1 %% 3000))), map('thread', concat('w-', toString(h2 %% 32)))
FROM numbers(%d)`, logsFrom.Format("2006-01-02 15:04:05"), rows))
	e.Exec(t, "OPTIMIZE TABLE otel_logs FINAL")
	e.Exec(t, "OPTIMIZE TABLE otel_logs_rollup FINAL")
	t.Logf("BENCH seeded %d logs in %s", rows, time.Since(start).Round(time.Second))

	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "bench"})
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()
	span := time.Duration(rows/10) * time.Second // 100 ms apart
	from, to := logsFrom, logsFrom.Add(span).Truncate(time.Minute).Add(time.Minute)
	q := func(step timeseries.Duration, f ...clickhouse.LogFilter) clickhouse.LogQuery {
		return clickhouse.LogQuery{Ctx: timeseries.NewContext(timeseries.Time(from.Unix()), timeseries.Time(to.Unix()), step), Filters: f, Limit: 100}
	}
	agent := clickhouse.LogFilter{Name: "Source", Op: "=", Value: "agent"}

	for _, tc := range []struct {
		name string
		q    clickhouse.LogQuery
	}{
		{"list, no search", q(300, agent)},
		{"list, search: common words (timeout retrying)", q(300, agent, search("contains", "timeout retrying"))},
		{"list, search: rare id (ord-715688382)", q(300, search("contains", "ord-715688382"))},
		{"list, search: fragment with * (timeo*)", q(300, agent, search("contains", "timeo*"))},
	} {
		query := tc.q
		benchBest(t, tc.name, 3, func() {
			_, err := c.GetLogs(ctx, query)
			require.NoError(t, err)
		})
	}
	hist := q(300, agent)
	benchBest(t, "histogram, rollup", 3, func() { _, err := c.GetLogsHistogram(ctx, hist); require.NoError(t, err) })
	benchBest(t, "histogram, raw table", 3, func() { _, err := c.GetLogsHistogram(ctx, rawQuery(hist)); require.NoError(t, err) })
	var names []string
	for _, facet := range []string{"Severity", "service.name", "host.name", "Source", "Namespace", "Application"} {
		facet := facet
		names = append(names, facet)
		benchBest(t, "facet "+facet+", rollup", 3, func() { _, err := c.GetLogFacetCounts(ctx, hist, facet); require.NoError(t, err) })
		benchBest(t, "facet "+facet+", raw table", 3, func() { _, err := c.GetLogFacetCounts(ctx, rawQuery(hist), facet); require.NoError(t, err) })
	}
	sort.Strings(names)
}

// rawQuery adds a filter that matches every row but rules the rollup out.
func rawQuery(q clickhouse.LogQuery) clickhouse.LogQuery {
	q.Filters = append(append([]clickhouse.LogFilter{}, q.Filters...), rawOnly)
	return q
}

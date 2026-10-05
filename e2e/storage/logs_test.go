//go:build e2e

package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var logsFrom = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

// seedLogs inserts n logs one second apart. Row i says one of a few things; one
// row carries a unique order id and one a word that only occurs inside a longer
// word, so every search below has a known answer.
func seedLogs(t *testing.T, e *chtest.Env, n int) {
	t.Helper()
	e.Exec(t, fmt.Sprintf(`
INSERT INTO otel_logs (Timestamp, TraceId, SpanId, TraceFlags, SeverityText, SeverityNumber, ServiceName, Body, ResourceAttributes, LogAttributes)
SELECT toDateTime64('%s', 9) + number, '', '', 0, 'INFO', 9, '/k8s/ns/app',
  multiIf(
    number %% 10 = 0, concat('connection to db-', toString(number %% 7), ':5432 failed: Timeout after ', toString(number %% 30), 's'),
    number %% 10 = 1, concat('GET /api/orders/', toString(number), ' 200 in ', toString(number %% 900), 'ms'),
    number %% 10 = 2, 'request failure: upstream unavailable',
    number = 123453, 'order ord-715688382 shipped',
    number = 123454, 'unrelated PreTimeoutHandler started',
    concat('worker ', toString(number %% 50), ' heartbeat')),
  map('host.name', 'node-a'), map()
FROM numbers(%d)`, logsFrom.Format("2006-01-02 15:04:05"), n))
	e.Exec(t, "OPTIMIZE TABLE otel_logs FINAL")
}

func logQuery(filters ...clickhouse.LogFilter) clickhouse.LogQuery {
	from := logsFrom.Add(-time.Hour)
	to := logsFrom.Add(100 * time.Hour)
	return clickhouse.LogQuery{
		Ctx:     timeseries.NewContext(timeseries.Time(from.Unix()), timeseries.Time(to.Unix()), 60),
		Filters: filters,
		Limit:   1000000,
	}
}

func search(op, value string) clickhouse.LogFilter {
	return clickhouse.LogFilter{Name: "Message", Op: op, Value: value}
}

func TestLogSearchAgainstClickHouse(t *testing.T) {
	const n = 300000
	e := chtest.New(t)
	seedLogs(t, e, n)
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	count := func(f ...clickhouse.LogFilter) int {
		t.Helper()
		logs, err := c.GetLogs(ctx, logQuery(f...))
		require.NoError(t, err)
		return len(logs)
	}
	// ground truth by plain SQL, independent of the search code
	truth := func(cond string) int {
		t.Helper()
		var got uint64
		require.NoError(t, c.QueryRow(ctx, "SELECT count() FROM @@table_otel_logs@@ WHERE "+cond).Scan(&got))
		return int(got)
	}
	timeouts := truth("positionCaseInsensitiveUTF8(Body, 'timeout') > 0")
	require.Greater(t, timeouts, 0)

	t.Run("a word matches whole words, any case", func(t *testing.T) {
		// 'PreTimeoutHandler' contains the text "timeout" inside one word: not a hit
		assert.Equal(t, truth("Body LIKE '%Timeout after%'"), count(search("contains", "timeout")))
		assert.Equal(t, count(search("contains", "timeout")), count(search("contains", "TIMEOUT")))
		assert.Equal(t, timeouts-1, count(search("contains", "timeout")))
	})
	t.Run("a fragment of a word does not match", func(t *testing.T) {
		assert.Equal(t, 0, count(search("contains", "timeo")))
	})
	t.Run("a star makes it a substring search", func(t *testing.T) {
		assert.Equal(t, timeouts, count(search("contains", "timeo*")))
		assert.Equal(t, timeouts, count(search("contains", "*imeout")))
		assert.Equal(t, 1, count(search("contains", "*imeoutHand*")))
	})
	t.Run("punctuation splits words and all words must occur", func(t *testing.T) {
		assert.Equal(t, truth("Body LIKE 'connection to db-3:5432 %'"), count(search("contains", "db-3:5432")))
		assert.Equal(t, 0, count(search("contains", "db-3 heartbeat")))
	})
	t.Run("a rare id is found", func(t *testing.T) {
		logs, err := c.GetLogs(ctx, logQuery(search("contains", "ord-715688382")))
		require.NoError(t, err)
		require.Len(t, logs, 1)
		assert.Equal(t, "order ord-715688382 shipped", logs[0].Body)
	})
	t.Run("not contains", func(t *testing.T) {
		assert.Equal(t, n-count(search("contains", "heartbeat")), count(search("not contains", "heartbeat")))
		assert.Equal(t, n-timeouts, count(search("not contains", "timeo*")))
	})
	t.Run("two search filters are ANDed", func(t *testing.T) {
		assert.Equal(t, truth("Body LIKE 'connection to db-3:5432 %'"), count(search("contains", "db-3"), search("contains", "5432 Timeout")))
	})
}

// The point of the index: a search for a rare word must skip almost every
// granule.
func TestLogSearchUsesTextIndex(t *testing.T) {
	e := chtest.New(t)
	seedLogs(t, e, 300000)
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()

	// the expression is the one the product builds for a word search (pinned by
	// TestLogQueryFiltersMessageSearch in package clickhouse)
	rows, err := c.Query(context.Background(), "EXPLAIN indexes = 1 SELECT max(length(Body)) FROM otel_logs WHERE hasAllTokens(lowerUTF8(Body), ['ord', '715688382'])")
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan = append(plan, line)
	}
	text := strings.Join(plan, "\n")
	t.Log(text)
	require.Contains(t, text, "idx_body", "the text index is not used")

	// The query reads Body, so it cannot be answered from the index alone (a
	// count() is, since 26.9: ReadFromTextIndexCount). In the plan, the skip
	// index section "Name: idx_body" is followed by "Granules: kept/total".
	var kept, total int
	for i, line := range plan {
		if strings.TrimSpace(line) == "Name: idx_body" {
			for _, l := range plan[i:] {
				if _, err := fmt.Sscanf(strings.TrimSpace(l), "Granules: %d/%d", &kept, &total); err == nil {
					break
				}
			}
			break
		}
	}
	require.Greater(t, total, 20)
	assert.LessOrEqual(t, kept, total/10, "the index should drop almost every granule for a rare word")
}

// The rollup must give exactly the numbers the raw table gives. Rows differ in
// every dimension the rollup keeps, with host.name sometimes on the log,
// sometimes on the resource, and sometimes on both with different values.
func seedRollupLogs(t *testing.T, e *chtest.Env, n int) {
	t.Helper()
	e.Exec(t, fmt.Sprintf(`
INSERT INTO otel_logs (Timestamp, TraceId, SpanId, TraceFlags, SeverityText, SeverityNumber, ServiceName, Body, ResourceAttributes, LogAttributes)
SELECT toDateTime64('%s', 9) + toIntervalMillisecond(number * 100 + 37), '', '', 0, 'x',
  [0, 5, 9, 13, 17][1 + number %% 5],
  multiIf(number %% 4 = 0, '/k8s/ns-a/api', number %% 4 = 1, '/k8s/ns-b/worker', number %% 4 = 2, 'otel-checkout', 'otel-search'),
  concat('m', toString(number)),
  multiIf(number %% 3 = 0, map('host.name', concat('res-', toString(number %% 4))), number %% 3 = 1, map('host.name', 'both-res'), map()),
  multiIf(number %% 6 = 1, map('host.name', 'both-log'), number %% 6 = 3, map('host.name', concat('log-', toString(number %% 3))), map())
FROM numbers(%d)`, logsFrom.Format("2006-01-02 15:04:05"), n))
	e.Exec(t, "OPTIMIZE TABLE otel_logs_rollup FINAL")
}

// rawOnly matches every row but names an attribute the rollup does not keep, so
// a query that carries it is answered from the raw table. It gives the numbers
// to compare the rollup with.
var rawOnly = clickhouse.LogFilter{Name: "no.such.attribute", Op: "!=", Value: "zzz"}

func TestLogRollupMatchesRawTable(t *testing.T) {
	const n = 60000 // 100 ms apart: about 100 minutes
	e := chtest.New(t)
	seedRollupLogs(t, e, n)
	c, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	var rollupRows, rawRows uint64
	require.NoError(t, c.QueryRow(ctx, "SELECT sum(Count) FROM @@table_otel_logs_rollup@@").Scan(&rollupRows))
	require.NoError(t, c.QueryRow(ctx, "SELECT count() FROM @@table_otel_logs@@").Scan(&rawRows))
	require.Equal(t, rawRows, rollupRows, "the materialized view sees every insert")
	var rollupSize uint64
	require.NoError(t, c.QueryRow(ctx, "SELECT count() FROM @@table_otel_logs_rollup@@").Scan(&rollupSize))
	require.Less(t, rollupSize, rawRows/2, "the rollup is much smaller than the raw table")

	windows := []struct {
		name     string
		from, to time.Time
	}{
		{"half an hour", logsFrom.Add(10 * time.Minute), logsFrom.Add(40 * time.Minute)},
		{"one hour", logsFrom.Add(5 * time.Minute), logsFrom.Add(65 * time.Minute)},
		{"everything", logsFrom.Add(-time.Hour), logsFrom.Add(3 * time.Hour)},
	}
	filterSets := map[string][]clickhouse.LogFilter{
		"none":           nil,
		"severity":       {{Name: "Severity", Op: "=", Value: "error"}},
		"not severity":   {{Name: "Severity", Op: "!=", Value: "info"}},
		"source agent":   {{Name: "Source", Op: "=", Value: "agent"}},
		"service regex":  {{Name: "service.name", Op: "~", Value: "^/k8s/ns-a"}},
		"service not":    {{Name: "service.name", Op: "!=", Value: "otel-search"}},
		"namespace":      {{Name: "Namespace", Op: "=", Value: "ns-b"}},
		"not namespace":  {{Name: "Namespace", Op: "!=", Value: "n/a"}},
		"application":    {{Name: "Application", Op: "=", Value: "worker"}},
		"host log":       {{Name: "host.name", Op: "=", Value: "log-1"}},
		"host both":      {{Name: "host.name", Op: "=", Value: "both-res"}},
		"host not":       {{Name: "host.name", Op: "!=", Value: "both-log"}},
		"host regex":     {{Name: "host.name", Op: "~", Value: "^res-"}},
		"host not regex": {{Name: "host.name", Op: "!~", Value: "^res-"}},
		"combined":       {{Name: "Severity", Op: "=", Value: "error"}, {Name: "host.name", Op: "=", Value: "res-2"}, {Name: "Namespace", Op: "=", Value: "ns-a"}},
	}
	facets := []string{"Severity", "service.name", "host.name", "Cluster", "Source", "Namespace", "Application"}

	checked := 0
	for _, w := range windows {
		for fname, filters := range filterSets {
			q := clickhouse.LogQuery{
				Ctx:     timeseries.NewContext(timeseries.Time(w.from.Unix()), timeseries.Time(w.to.Unix()), 300),
				Filters: filters,
			}
			fast, err := c.GetLogsHistogram(ctx, q)
			require.NoError(t, err)
			rawQ := q
			rawQ.Filters = append(append([]clickhouse.LogFilter{}, q.Filters...), rawOnly)
			raw, err := c.GetLogsHistogram(ctx, rawQ)
			require.NoError(t, err)
			assertHistogramsEqual(t, raw, fast, w.name+"/"+fname)

			for _, facet := range facets {
				fast, err := c.GetLogFacetCounts(ctx, q, facet)
				require.NoError(t, err)
				raw, err := c.GetLogFacetCounts(ctx, rawQ, facet)
				require.NoError(t, err)
				assert.Equal(t, raw, fast, "facet %s, %s/%s", facet, w.name, fname)
				checked++
			}
		}
	}
	t.Logf("compared %d facet results and as many histograms", checked)
}

func assertHistogramsEqual(t *testing.T, raw, fast []model.LogHistogramBucket, msg string) {
	t.Helper()
	require.Equal(t, len(raw), len(fast), msg)
	for i := range raw {
		assert.Equal(t, raw[i].Severity, fast[i].Severity, msg)
		rawJSON, err := raw[i].Timeseries.MarshalJSON()
		require.NoError(t, err)
		fastJSON, err := fast[i].Timeseries.MarshalJSON()
		require.NoError(t, err)
		assert.JSONEq(t, string(rawJSON), string(fastJSON), "%s: severity %v", msg, raw[i].Severity)
	}
}

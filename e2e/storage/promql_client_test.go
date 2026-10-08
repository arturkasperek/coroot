//go:build e2e

package storage

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/collector"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/promql"
	"github.com/coroot/coroot/timeseries"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func byLabel(res []*model.MetricValues, label string) map[string]*model.MetricValues {
	out := map[string]*model.MetricValues{}
	for _, mv := range res {
		out[mv.Labels[label]] = mv
	}
	return out
}

// Metrics go in through the real collector and come out through the PromQL
// client (the Prometheus engine is gone: ClickHouse evaluates the queries).
func TestPromQLClientRoundTrip(t *testing.T) {
	e := chtest.New(t)
	ll := e.LL
	ctx := context.Background()

	end := time.Now().Truncate(time.Minute)
	start := end.Add(-10 * time.Minute)
	samples := func(n int, f func(i int) float64) []prompb.Sample {
		var out []prompb.Sample
		for i := 0; i < n; i++ { // every 15 s
			out = append(out, prompb.Sample{Timestamp: start.Add(time.Duration(i) * 15 * time.Second).UnixMilli(), Value: f(i)})
		}
		return out
	}
	batch := collector.NewMetricsBatch(1_000_000, time.Hour, func(q ch.Query) error { return ll.Do(ctx, q) })
	for _, s := range []prompb.TimeSeries{
		ts("up", map[string]string{"job": "api", "instance": "i1", "namespace": "a", "empty": ""}, samples(41, func(int) float64 { return 1 })),
		ts("up", map[string]string{"job": "api", "instance": "i2", "namespace": "a"}, samples(41, func(int) float64 { return 0 })),
		ts("up", map[string]string{"job": "db", "instance": "i3", "namespace": "b"}, samples(41, func(int) float64 { return 1 })),
		ts("http_requests_total", map[string]string{"job": "api", "instance": "i1", "namespace": "a"}, samples(41, func(i int) float64 { return float64(i) * 15 })),
		ts("http_requests_total", map[string]string{"job": "api", "instance": "i2", "namespace": "a"}, samples(41, func(i int) float64 { return float64(i) * 30 })),
		// ends after 2 minutes
		ts("short_lived", map[string]string{"job": "x"}, samples(9, func(int) float64 { return 7 })),
	} {
		batch.Add(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{s}, Metadata: []prompb.MetricMetadata{
			{MetricFamilyName: "up", Type: prompb.MetricMetadata_GAUGE, Help: "is up"},
		}})
	}
	batch.Close()

	cc, err := clickhouse.NewClient(e.Config, &db.Project{Name: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	client := promql.New(cc, 15*timeseries.Second)

	from, to := timeseries.TimeFromStandard(start.Add(time.Minute)), timeseries.TimeFromStandard(end.Add(-time.Minute))
	q := func(query string) []*model.MetricValues {
		t.Helper()
		res, err := client.QueryRange(ctx, query, promql.FilterLabelsKeepAll, from, to, 15)
		require.NoError(t, err, query)
		return res
	}
	// replicated: wait for the node this connection landed on
	require.Eventually(t, func() bool {
		res, err := client.QueryRange(ctx, `up`, promql.FilterLabelsKeepAll, from, to, 15)
		return err == nil && len(res) == 3
	}, 20*time.Second, 300*time.Millisecond)

	t.Run("a metric with all its series; written twice would not duplicate them", func(t *testing.T) {
		res := byLabel(q("up"), "instance")
		require.Len(t, res, 3)
		assert.Equal(t, float32(1), res["i1"].Values.Last())
		assert.Equal(t, float32(0), res["i2"].Values.Last())
		assert.Equal(t, "db", res["i3"].Labels["job"])
		_, hasName := res["i1"].Labels["__name__"]
		assert.False(t, hasName, "the metric name is not a label")
		_, hasEmpty := res["i1"].Labels["empty"]
		assert.False(t, hasEmpty)
	})
	t.Run("matchers", func(t *testing.T) {
		require.Len(t, q(`up{job="api"}`), 2)
		require.Len(t, q(`up{namespace="b"}`), 1)
		require.Len(t, q(`up{job!="api"}`), 1)
		require.Len(t, q(`up{instance=~"i[12]"}`), 2)
		require.Len(t, q(`up{instance!~"i[12]"}`), 1)
		require.Len(t, q(`up{missing="x"}`), 0)
		// the metric name is not a label, so up{i1} and http_requests_total{i1} are one series here
		require.Len(t, q(`{__name__=~"up|http_requests_total"}`), 3)
		require.Len(t, q(`{job="db"}`), 1)
	})
	t.Run("regexes are anchored and an empty label is an absent label", func(t *testing.T) {
		assert.Len(t, q(`up{namespace=~"a"}`), 2)
		assert.Empty(t, q(`up{namespace=~"a."}`))
		assert.Len(t, q(`up{missing=""}`), 3, "no series has this label")
		assert.Empty(t, q(`up{job=""}`))
	})
	t.Run("functions", func(t *testing.T) {
		res := byLabel(q(`rate(http_requests_total[1m])`), "instance")
		require.Len(t, res, 2)
		assert.InDelta(t, 1.0, res["i1"].Values.Last(), 0.01) // 15 per 15 s
		assert.InDelta(t, 2.0, res["i2"].Values.Last(), 0.01)
		sum := q(`sum by (job) (rate(http_requests_total[1m]))`)
		require.Len(t, sum, 1)
		assert.InDelta(t, 3.0, sum[0].Values.Last(), 0.02)
	})
	t.Run("$RANGE is three steps", func(t *testing.T) {
		res := byLabel(q(`rate(http_requests_total[$RANGE])`), "instance")
		assert.InDelta(t, 1.0, res["i1"].Values.Last(), 0.01)
	})
	t.Run("the labels the caller does not want are dropped and series that collapse are merged", func(t *testing.T) {
		res, err := client.QueryRange(ctx, `up`, func(n string) bool { return n == "job" }, from, to, 15)
		require.NoError(t, err)
		got := byLabel(res, "job")
		require.Len(t, got, 2, "i1 and i2 have the same labels once instance is dropped")
		assert.Equal(t, "api", got["api"].Labels["job"])
		_, hasInstance := got["api"].Labels["instance"]
		assert.False(t, hasInstance)
	})
	t.Run("a series that has ended has no points after its end", func(t *testing.T) {
		res := q(`short_lived`)
		require.Len(t, res, 1)
		assert.True(t, timeseries.IsNaN(res[0].Values.Last()), "no value 8 minutes after the last sample")
		_, v := res[0].Values.LastNotNull()
		assert.Equal(t, float32(7), v)
	})

	t.Run("metric names", func(t *testing.T) {
		names, err := client.MetricNames(ctx)
		require.NoError(t, err)
		assert.Equal(t, map[string]bool{"up": true, "http_requests_total": true, "short_lived": true}, names)
	})

	get := func(h func(w *httptest.ResponseRecorder, r *url.Values), form url.Values) map[string]any {
		w := httptest.NewRecorder()
		h(w, &form)
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
		require.Equal(t, "success", out["status"], w.Body.String())
		return out
	}
	strs := func(v any) []string {
		var out []string
		for _, x := range v.([]any) {
			out = append(out, x.(string))
		}
		sort.Strings(out)
		return out
	}
	t.Run("label values", func(t *testing.T) {
		values := func(label string, matchers ...string) []string {
			form := url.Values{"match[]": matchers}
			out := get(func(w *httptest.ResponseRecorder, f *url.Values) {
				client.LabelValues(httptest.NewRequest("GET", "/?"+f.Encode(), nil), w, label)
			}, form)
			return strs(out["data"])
		}
		assert.Equal(t, []string{"api", "db", "x"}, values("job"))
		assert.Equal(t, []string{"api"}, values("job", `http_requests_total`))
		assert.Equal(t, []string{"http_requests_total", "short_lived", "up"}, values("__name__"))
		assert.Equal(t, []string{"a"}, values("namespace", `{namespace=~"a"}`), "anchored")
		assert.Empty(t, values("namespace", `{namespace=~"a."}`))
	})
	t.Run("series", func(t *testing.T) {
		out := get(func(w *httptest.ResponseRecorder, f *url.Values) {
			client.Series(httptest.NewRequest("GET", "/?"+f.Encode(), nil), w)
		}, url.Values{"match[]": {`up{job="api"}`}})
		series := out["data"].([]any)
		require.Len(t, series, 2)
		assert.Equal(t, "up", series[0].(map[string]any)["__name__"])
	})
	t.Run("metadata", func(t *testing.T) {
		out := get(func(w *httptest.ResponseRecorder, f *url.Values) {
			client.MetricMetadata(httptest.NewRequest("GET", "/?"+f.Encode(), nil), w)
		}, url.Values{})
		md := out["data"].(map[string]any)["up"].([]any)[0].(map[string]any)
		assert.Equal(t, "gauge", md["type"])
		assert.Equal(t, "is up", md["help"])
	})
	t.Run("query_range in the Prometheus format", func(t *testing.T) {
		out := get(func(w *httptest.ResponseRecorder, f *url.Values) {
			client.QueryRangeHandler(httptest.NewRequest("GET", "/?"+f.Encode(), nil), w)
		}, url.Values{"query": {`up{instance="i1"}`}, "start": {fmtUnix(from)}, "end": {fmtUnix(to)}, "step": {"15"}})
		data := out["data"].(map[string]any)
		assert.Equal(t, "matrix", data["resultType"])
		result := data["result"].([]any)
		require.Len(t, result, 1)
		first := result[0].(map[string]any)["values"].([]any)[0].([]any)
		assert.Equal(t, "1", first[1])
	})
}

func fmtUnix(t timeseries.Time) string { return time.Unix(int64(t), 0).UTC().Format(time.RFC3339) }

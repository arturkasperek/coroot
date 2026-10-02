//go:build e2e

package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/coroot/coroot/ch/chtest"
	"github.com/coroot/coroot/collector"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/prom"
	"github.com/coroot/coroot/timeseries"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ts(name string, lbls map[string]string, samples []prompb.Sample) prompb.TimeSeries {
	ls := []prompb.Label{{Name: "__name__", Value: name}}
	for k, v := range lbls {
		ls = append(ls, prompb.Label{Name: k, Value: v})
	}
	return prompb.TimeSeries{Labels: ls, Samples: samples}
}

func byLabel(res []*model.MetricValues, label string) map[string]*model.MetricValues {
	out := map[string]*model.MetricValues{}
	for _, mv := range res {
		out[mv.Labels[label]] = mv
	}
	return out
}

func last(mv *model.MetricValues) float32 { return mv.Values.Last() }

// Metrics go in through the real collector and come out through the PromQL
// engine on top of the real storage layer.
func TestMetricsRoundTrip(t *testing.T) {
	e := chtest.New(t)
	ll := e.LL
	ctx := context.Background()

	end := time.Now().Truncate(time.Minute)
	start := end.Add(-10 * time.Minute)
	samples := func(f func(i int) float64) []prompb.Sample {
		var out []prompb.Sample
		for i := 0; i <= 40; i++ { // every 15 s
			out = append(out, prompb.Sample{Timestamp: start.Add(time.Duration(i) * 15 * time.Second).UnixMilli(), Value: f(i)})
		}
		return out
	}
	// two batches, so every series is written twice (unmerged repeats in the series table)
	for part := 0; part < 2; part++ {
		batch := collector.NewMetricsBatch(1_000_000, time.Hour, func(q ch.Query) error { return ll.Do(ctx, q) })
		all := []prompb.TimeSeries{
			ts("up", map[string]string{"job": "api", "instance": "i1", "namespace": "a"}, samples(func(int) float64 { return 1 })),
			ts("up", map[string]string{"job": "api", "instance": "i2", "namespace": "a"}, samples(func(int) float64 { return 0 })),
			ts("up", map[string]string{"job": "db", "instance": "i3", "namespace": "b"}, samples(func(int) float64 { return 1 })),
			ts("http_requests_total", map[string]string{"job": "api", "instance": "i1", "namespace": "a"}, samples(func(i int) float64 { return float64(i) * 15 })),
			ts("http_requests_total", map[string]string{"job": "api", "instance": "i2", "namespace": "a"}, samples(func(i int) float64 { return float64(i) * 30 })),
		}
		for _, s := range all {
			half := len(s.Samples) / 2
			if part == 0 {
				s.Samples = s.Samples[:half]
			} else {
				s.Samples = s.Samples[half:]
			}
			batch.Add(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{s}})
		}
		batch.Close()
	}

	client, err := prom.NewClient(&db.IntegrationPrometheus{UseClickHouse: true, RefreshInterval: 15}, e.Config)
	require.NoError(t, err)
	defer client.Close()
	from, to := timeseries.TimeFromStandard(start.Add(time.Minute)), timeseries.TimeFromStandard(end.Add(-time.Minute))
	q := func(query string) []*model.MetricValues {
		t.Helper()
		res, err := client.QueryRange(ctx, query, prom.FilterLabelsKeepAll, from, to, 15)
		require.NoError(t, err, query)
		return res
	}

	t.Run("a metric with all its series", func(t *testing.T) {
		res := byLabel(q("up"), "instance")
		require.Len(t, res, 3, "the repeated series rows give no duplicate series")
		assert.Equal(t, float32(1), last(res["i1"]))
		assert.Equal(t, float32(0), last(res["i2"]))
		assert.Equal(t, "db", res["i3"].Labels["job"])
		assert.Equal(t, "up", res["i1"].Labels["__name__"])
	})
	t.Run("equality matcher on a label", func(t *testing.T) {
		require.Len(t, q(`up{job="api"}`), 2)
		require.Len(t, q(`up{namespace="b"}`), 1)
	})
	t.Run("negative and regex matchers", func(t *testing.T) {
		require.Len(t, q(`up{job!="api"}`), 1)
		require.Len(t, q(`up{instance=~"i[12]"}`), 2)
		require.Len(t, q(`up{instance!~"i[12]"}`), 1)
		require.Len(t, q(`up{missing="x"}`), 0)
	})
	t.Run("a matcher on the metric name", func(t *testing.T) {
		require.Len(t, q(`{__name__=~"up|http_requests_total"}`), 5)
		require.Len(t, q(`{__name__="nope"}`), 0)
	})
	t.Run("a selector with no metric name", func(t *testing.T) {
		require.Len(t, q(`{job="db"}`), 1)
	})
	t.Run("functions over the samples", func(t *testing.T) {
		res := byLabel(q(`rate(http_requests_total[1m])`), "instance")
		require.Len(t, res, 2)
		assert.InDelta(t, 1.0, last(res["i1"]), 0.01) // 15 per 15 s
		assert.InDelta(t, 2.0, last(res["i2"]), 0.01)
		sum := q(`sum by (job) (rate(http_requests_total[1m]))`)
		require.Len(t, sum, 1)
		assert.InDelta(t, 3.0, last(sum[0]), 0.02)
	})

	t.Run("label values come from the series table", func(t *testing.T) {
		get := func(label string, matchers ...string) []string {
			form := url.Values{}
			for _, m := range matchers {
				form.Add("match[]", m)
			}
			r := httptest.NewRequest(http.MethodGet, "/api/v1/label/"+label+"/values?"+form.Encode(), nil)
			w := httptest.NewRecorder()
			client.LabelValues(r, w, label)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			return jsonStrings(t, w.Body.Bytes())
		}
		assert.Equal(t, []string{"api", "db"}, get("job"))
		assert.Equal(t, []string{"api"}, get("job", `http_requests_total`))
		assert.Equal(t, []string{"http_requests_total", "up"}, get("__name__"))
	})
}

func jsonStrings(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Data []string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp), string(body))
	sort.Strings(resp.Data)
	return resp.Data
}

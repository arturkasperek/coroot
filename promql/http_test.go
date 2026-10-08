package promql

import (
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatrixDropsNaNAndEmptySeries(t *testing.T) {
	res := matrix([]rawSeries{
		{tags: map[string]string{"__name__": "up", "job": "a"}, ts: []uint32{15, 30, 45}, vs: []float64{1, math.NaN(), 0.5}},
		{tags: map[string]string{"__name__": "gone"}, ts: []uint32{15}, vs: []float64{math.NaN()}},
	})
	require.Len(t, res, 1, "a series with no point is not in the result")
	assert.Equal(t, map[string]string{"__name__": "up", "job": "a"}, res[0].Metric)
	assert.Equal(t, [][2]interface{}{{float64(15), "1"}, {float64(45), "0.5"}}, res[0].Values)
	assert.NotNil(t, matrix(nil), "an empty result is [], not null")
}

func TestParseTimeAndDuration(t *testing.T) {
	ts, err := parseTime("1790000000.5")
	require.NoError(t, err)
	assert.Equal(t, int64(1790000000), ts.Unix())
	ts, err = parseTime("2026-10-05T08:00:00Z")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC), ts)
	_, err = parseTime("yesterday")
	assert.Error(t, err)

	d, err := parseDuration("15")
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, d)
	d, err = parseDuration("5m")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, d)
	_, err = parseDuration("soon")
	assert.Error(t, err)
}

func TestQueryRangeHandlerRejectsBadRequests(t *testing.T) {
	c := &Client{} // the requests below fail before ClickHouse is asked
	for name, form := range map[string]string{
		"no start":         "end=2&step=15&query=up",
		"end before start": "start=20&end=10&step=15&query=up",
		"zero step":        "start=1&end=2&step=0&query=up",
		"too many points":  "start=0&end=1000000&step=1&query=up",
	} {
		w := httptest.NewRecorder()
		c.QueryRangeHandler(httptest.NewRequest("GET", "/?"+form, nil), w)
		assert.Contains(t, w.Body.String(), `"status":"error"`, name)
		assert.Contains(t, w.Body.String(), `"errorType":"bad_data"`, name)
	}
}

func TestLabelValuesRejectsAnInvalidLabelName(t *testing.T) {
	w := httptest.NewRecorder()
	(&Client{}).LabelValues(httptest.NewRequest("GET", "/", nil), w, "not a label")
	assert.Contains(t, w.Body.String(), `"errorType":"bad_data"`)
}

func TestSeriesNeedsAMatcher(t *testing.T) {
	w := httptest.NewRecorder()
	(&Client{}).Series(httptest.NewRequest("GET", "/", nil), w)
	assert.Contains(t, w.Body.String(), "no match[] parameter provided")
}

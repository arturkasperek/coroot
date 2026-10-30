package constructor

import (
	"context"
	"reflect"
	"testing"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSLIs(t *testing.T) {
	project := &db.Project{Id: "p"}
	shopId := model.NewApplicationId("", "prod", model.ApplicationKindDeployment, "shop")
	clientId := model.NewApplicationId("", "prod", model.ApplicationKindDeployment, "client")
	newWorld := func() (*model.World, *model.Application) {
		// a page of 30 min at 10 min: 4 points
		w := model.NewWorld(0, 1800, 600, 15)
		w.CheckConfigs = model.CheckConfigs{}
		w.GetOrCreateApplication(clientId, false)
		return w, w.GetOrCreateApplication(shopId, false)
	}
	series := func(labels model.Labels, step timeseries.Duration, values ...float32) *model.MetricValues {
		return &model.MetricValues{Labels: labels, Values: timeseries.NewWithData(0, step, values)}
	}
	// the requests to shop at the page step (averages of each 10 min), as the server sees them
	pageSLIs := map[string][]*model.MetricValues{
		qRecordingRuleApplicationL7InboundRequests + sliSuffix: {
			series(model.Labels{"app": shopId.String(), "status": "200"}, 600, 10, 20, 30, 40),
			series(model.Labels{"app": shopId.String(), "status": "500"}, 600, 1, 2, 3, 4),
		},
		qRecordingRuleApplicationL7InboundHistogram + sliSuffix: {
			series(model.Labels{"app": shopId.String(), "le": "+Inf"}, 600, 11, 22, 33, 44),
			series(model.Labels{"app": shopId.String(), "le": "0.1"}, 600, 9, 18, 27, 36),
		},
	}

	t.Run("a page reads the SLIs at its own step, with no raw ones", func(t *testing.T) {
		w, shop := newWorld()
		(&Constructor{}).loadSLIs(w, pageSLIs, project)

		require.Len(t, shop.AvailabilitySLIs, 1)
		availability := shop.AvailabilitySLIs[0]
		assert.Equal(t, "TimeSeries(0, 4, 600, [11 22 33 44])", availability.TotalRequests.String(), "every status")
		assert.Equal(t, "TimeSeries(0, 4, 600, [1 2 3 4])", availability.FailedRequests.String(), "the 5xx")
		assert.Nil(t, availability.TotalRequestsRaw)
		assert.Nil(t, availability.FailedRequestsRaw)

		require.Len(t, shop.LatencySLIs, 1)
		latency := shop.LatencySLIs[0]
		require.Len(t, latency.Histogram, 2)
		assert.Equal(t, float32(0.1), latency.Histogram[0].Le, "sorted by le")
		assert.Equal(t, "TimeSeries(0, 4, 600, [9 18 27 36])", latency.Histogram[0].TimeSeries.String())
		assert.Empty(t, latency.HistogramRaw)
	})

	t.Run("with OptionLoadRawSLIs the raw ones are there too", func(t *testing.T) {
		metrics := map[string][]*model.MetricValues{
			qRecordingRuleApplicationL7InboundRequests + sliRawSuffix: {
				series(model.Labels{"app": shopId.String(), "status": "200"}, 15, 5, 6),
				series(model.Labels{"app": shopId.String(), "status": "502"}, 15, 1, 0),
			},
			qRecordingRuleApplicationL7InboundHistogram + sliRawSuffix: {
				series(model.Labels{"app": shopId.String(), "le": "+Inf"}, 15, 6, 6),
			},
		}
		for name, values := range pageSLIs {
			metrics[name] = values
		}
		w, shop := newWorld()
		(&Constructor{}).loadSLIs(w, metrics, project)

		availability := shop.AvailabilitySLIs[0]
		assert.Equal(t, "TimeSeries(0, 4, 600, [11 22 33 44])", availability.TotalRequests.String())
		assert.Equal(t, "TimeSeries(0, 2, 15, [6 6])", availability.TotalRequestsRaw.String())
		assert.Equal(t, "TimeSeries(0, 2, 15, [1 0])", availability.FailedRequestsRaw.String())
		require.Len(t, shop.LatencySLIs[0].HistogramRaw, 1)
	})

	t.Run("without inbound requests, the requests of the clients", func(t *testing.T) {
		w, shop := newWorld()
		(&Constructor{}).loadSLIs(w, map[string][]*model.MetricValues{
			qRecordingRuleApplicationL7Requests + sliSuffix: {
				series(model.Labels{"app": clientId.String(), "dest": shopId.String(), "status": "200"}, 600, 7, 7, 7, 7),
			},
		}, project)
		require.Len(t, shop.AvailabilitySLIs, 1)
		assert.Equal(t, "TimeSeries(0, 4, 600, [7 7 7 7])", shop.AvailabilitySLIs[0].TotalRequests.String())
		assert.Empty(t, shop.LatencySLIs, "no histogram: no latency SLI")
	})

	t.Run("an application with no requests has no SLI", func(t *testing.T) {
		w, shop := newWorld()
		(&Constructor{}).loadSLIs(w, map[string][]*model.MetricValues{}, project)
		assert.Empty(t, shop.AvailabilitySLIs)
		assert.Empty(t, shop.LatencySLIs)
	})
}

// requestsRecorder is a Cache that records what a run asks for.
type requestsRecorder struct {
	requests map[string]QueryRequest
}

func (r *requestsRecorder) QueryRanges(ctx context.Context, requests map[string]QueryRequest) (map[string][]*model.MetricValues, error) {
	r.requests = requests
	return nil, nil
}

func (r *requestsRecorder) GetStep(from, to timeseries.Time) (timeseries.Duration, error) {
	return 15, nil
}

func TestQueryCacheReadsRawSLIsOnlyWhenAsked(t *testing.T) {
	project := &db.Project{Id: "p"}
	// a page of 24 h at 10 min ending at 100000
	from, to := timeseries.Time(100000-24*3600), timeseries.Time(100000)
	sli := qRecordingRuleApplicationL7InboundRequests

	page := &requestsRecorder{}
	_, err := New(nil, project, nil, nil).queryCache(context.Background(), page, project, from, to, 600, 15, model.CheckConfigs{}, nil)
	require.NoError(t, err)
	assertRequest(t, QueryRequest{Query: sli, From: from.Truncate(600), To: to.Truncate(600), Step: 600, FillFunc: timeseries.FillAvg}, page.requests[sli+sliSuffix],
		"an SLI at the page step: the average of each step")
	assert.NotContains(t, page.requests, sli+sliRawSuffix)
	assert.NotContains(t, page.requests, sli, "not under the query's own name: rr_connection_l7_requests is also a connection query")

	watchers := &requestsRecorder{}
	_, err = New(nil, project, nil, nil, OptionLoadRawSLIs).queryCache(context.Background(), watchers, project, from, to, 600, 15, model.CheckConfigs{}, nil)
	require.NoError(t, err)
	assert.Contains(t, watchers.requests, sli+sliSuffix)
	assertRequest(t, QueryRequest{Query: sli, From: from.Truncate(15), To: to.Truncate(15), Step: 15, FillFunc: timeseries.FillAny}, watchers.requests[sli+sliRawSuffix],
		"the raw SLI over the page, or over the longest alert rule window if longer")
}

// assertRequest compares two requests, their fill functions by address (a func
// is not comparable).
func assertRequest(t *testing.T, expected, actual QueryRequest, message string) {
	t.Helper()
	assert.Equal(t, reflect.ValueOf(expected.FillFunc).Pointer(), reflect.ValueOf(actual.FillFunc).Pointer(), message)
	expected.FillFunc, actual.FillFunc = nil, nil
	assert.Equal(t, expected, actual, message)
}

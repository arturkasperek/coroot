package overview

import (
	"testing"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countsAt makes the counts of one duration bucket: a value per point, NaN
// where the bucket has no requests.
func countsAt(chartContext timeseries.Context, values ...float32) *timeseries.Aggregate {
	aggregate := timeseries.NewAggregate(timeseries.NanSum)
	aggregate.Add(timeseries.NewWithData(chartContext.From, chartContext.Step, values))
	return aggregate
}

func pointsOf(series *timeseries.TimeSeries) []float32 {
	var values []float32
	iterator := series.Iter()
	for iterator.Next() {
		_, value := iterator.Value()
		values = append(values, value)
	}
	return values
}

func TestLatencyQuantiles(t *testing.T) {
	nan := timeseries.NaN
	chartContext := timeseries.Context{From: 600, To: 780, Step: 60} // 4 points
	histogram := latencyHistogram(chartContext, map[float64]*timeseries.Aggregate{
		// point 0: 100 requests between 100 and 250 ms
		// point 1: 50 between 10 and 25 ms, 50 between 500 ms and 1 s
		// point 2: 10 slower than 10 s
		// point 3: none
		10:    countsAt(chartContext, nan, 50, nan, nan),
		100:   countsAt(chartContext, 100, nan, nan, nan),
		500:   countsAt(chartContext, nan, 50, nan, nan),
		10000: countsAt(chartContext, nan, nan, 10, nan),
	})
	require.Len(t, histogram, 12)
	assert.Equal(t, float32(0.005), histogram[0].Le)
	assert.True(t, timeseries.IsInf(histogram[11].Le, 1))
	assert.Equal(t, []float32{100, 100, 10, 0}, pointsOf(histogram[11].TimeSeries))

	p50 := pointsOf(model.Quantile(histogram, 0.5))
	p95 := pointsOf(model.Quantile(histogram, 0.95))
	// interpolated inside the bucket: half of 100..250 ms
	assert.InDelta(t, 0.175, p50[0], 0.0001)
	assert.InDelta(t, 0.2425, p95[0], 0.0001)
	// p50 is the top of the 10..25 ms bucket, p95 in the 500 ms..1 s one
	assert.InDelta(t, 0.025, p50[1], 0.0001)
	assert.InDelta(t, 0.95, p95[1], 0.0001)
	// slower than the last finite bound: reported as 10 s
	assert.Equal(t, float32(10), p50[2])
	assert.Equal(t, float32(10), p95[2])
	// no requests
	assert.True(t, timeseries.IsNaN(p50[3]))
	assert.True(t, timeseries.IsNaN(p95[3]))

	// 210 requests: 50 of 10..25 ms, 100 of 100..250 ms, 50 of 500 ms..1 s, 10
	// slower than 10 s; the 105th is the 55th of 100..250 ms, the 199.5th the
	// 49.5th of 500 ms..1 s
	wholeView := latencyQuantilesOfWholeView(histogram)
	require.NotNil(t, wholeView)
	assert.InDelta(t, 0.1825, wholeView.P50, 0.0001)
	assert.InDelta(t, 0.995, wholeView.P95, 0.0001)
}

func TestLatencyQuantilesWithoutRequests(t *testing.T) {
	chartContext := timeseries.Context{From: 600, To: 660, Step: 60}
	histogram := latencyHistogram(chartContext, map[float64]*timeseries.Aggregate{})
	assert.Nil(t, latencyQuantilesOfWholeView(histogram))
}

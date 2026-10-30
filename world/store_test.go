package world

import (
	"testing"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPointsSQLPicksTableByStep(t *testing.T) {
	from, to := timeseries.Time(1_790_000_100), timeseries.Time(1_790_003_700)

	// the stored resolution: the points as they are, no bucket and no aggregate
	raw := pointsSQL(from, to, 15, nil)
	assert.Contains(t, raw, "FROM @@table_world_points@@")
	assert.Contains(t, raw, "toStartOfHour(Timestamp) BETWEEN toStartOfHour(toDateTime(1790000100))")
	assert.Contains(t, raw, "Timestamp >= toDateTime(1790000100) AND Timestamp <= toDateTime(1790003700)")
	assert.Contains(t, raw, "SELECT Query, SeriesHash, toUInt32(Timestamp) AS T, Value AS L")
	assert.NotContains(t, raw, "argMax")
	assert.NotContains(t, raw, "toStartOfInterval")
	assert.NotContains(t, raw, "GROUP BY")

	r5 := pointsSQL(from, to, 15*timeseries.Minute, nil)
	assert.Contains(t, r5, "FROM @@table_world_points_5m@@")
	assert.Contains(t, r5, "toFloat32(argMaxMerge(Last)) AS L")
	assert.Contains(t, r5, "toFloat32(sum(Sum)/sum(Cnt)) AS A")
	assert.Contains(t, r5, "toFloat32(max(Max)) AS M")
	assert.Contains(t, r5, "toStartOfDay(Timestamp) BETWEEN")
	assert.Contains(t, r5, "INTERVAL 900 SECOND) + INTERVAL 900 SECOND")

	assert.Contains(t, pointsSQL(from, to, timeseries.Hour, nil), "FROM @@table_world_points_1h@@")
	assert.Contains(t, pointsSQL(from, to, 2*timeseries.Hour, nil), "FROM @@table_world_points_1h@@")

	// a step that is not a multiple of 5 min buckets the raw points
	r2 := pointsSQL(from, to, 2*timeseries.Minute, nil)
	assert.Contains(t, r2, "FROM @@table_world_points@@")
	assert.Contains(t, r2, "INTERVAL 120 SECOND")

	// bucketed: one row per point with the four aggregates, right-closed buckets
	assert.Contains(t, r2, "GROUP BY Query, SeriesHash, T")
	assert.Contains(t, r2, "toFloat32(argMax(Value, Timestamp)) AS L")
	assert.Contains(t, r2, "toFloat32(max(Value)) AS M")
	assert.Contains(t, r2, "toStartOfInterval(Timestamp - INTERVAL 1 SECOND, INTERVAL 120 SECOND) + INTERVAL 120 SECOND")
}

func TestPointsSQLReadsOnlyTheListedQueries(t *testing.T) {
	from, to := timeseries.Time(1_790_000_100), timeseries.Time(1_790_003_700)
	assert.NotContains(t, pointsSQL(from, to, 15, nil), "Query IN")
	assert.Contains(t, pointsSQL(from, to, 15, []string{"a", `up{job='x\y'}`}), `AND Query IN ('a', 'up{job=\'x\\y\'}')`)
	assert.Contains(t, pointsSQL(from, to, timeseries.Hour, []string{"a"}), "AND Query IN ('a')")
	assert.NotContains(t, seriesSQL(nil), "WHERE")
	assert.Contains(t, seriesSQL([]string{"a"}), "FINAL WHERE true AND Query IN ('a')")
}

func TestMetricValues(t *testing.T) {
	// a window of 1 h at 10 min, aligned to the step as windowOf makes it:
	// 7 points, 1800 2400 3000 3600 4200 4800 5400
	w := window{from: 1800, to: 5400, step: 600}

	// what read gives for one query: two series with the four aggregates of each bucket
	series := []*storedSeries{
		{
			labels: map[string]string{
				"app":          "shop",
				"machine_id":   "m1",
				"system_uuid":  "u1",
				"container_id": "/k8s/prod/shop-7d9/shop",
			},
			hash: 11,
			ts:   []uint32{1800, 3000, 3600}, // no point at 2400, nor after 3600
			last: []float32{1, 2, 3},
			avg:  []float32{10, 20, 30},
			sum:  []float32{100, 200, 300},
			max:  []float32{1000, 2000, 3000},
		},
		{
			labels: map[string]string{"destination": "10.0.0.5:5432", "actual_destination": "10.0.0.7:5432"},
			hash:   22,
			ts:     []uint32{5400},
			last:   []float32{7}, avg: []float32{7}, sum: []float32{7}, max: []float32{7},
		},
		{
			// a pod gone before the window: read, but with no point in it
			labels: map[string]string{"container_id": "/k8s/prod/shop-old/shop"},
			hash:   33,
		},
	}

	got := metricValues(series, w, timeseries.FillAny)
	require.Len(t, got, 2, "the series with no point is left out")

	container := got[0]
	assert.Equal(t, uint64(11), container.LabelsHash)
	assert.Equal(t, model.Labels{"app": "shop"}, container.Labels, "the labels the constructor reads from fields are not in Labels")
	assert.Equal(t, "m1", container.MachineID)
	assert.Equal(t, "u1", container.SystemUUID)
	assert.Equal(t, "/k8s/prod/shop-7d9/shop", container.ContainerId)
	// the whole window, NaN (.) where there is no point; FillAny: the last value of each bucket
	assert.Equal(t, "TimeSeries(1800, 7, 600, [1 . 2 3 . . .])", container.Values.String())

	connection := got[1]
	assert.Empty(t, connection.Labels)
	assert.Equal(t, "10.0.0.5:5432", connection.Destination)
	assert.Equal(t, "10.0.0.7:5432", connection.ActualDestination)
	assert.Equal(t, "TimeSeries(1800, 7, 600, [. . . . . . 7])", connection.Values.String())

	// the same rows give another aggregate for another fill function
	assert.Equal(t, "TimeSeries(1800, 7, 600, [10 . 20 30 . . .])", metricValues(series, w, timeseries.FillAvg)[0].Values.String())
	assert.Equal(t, "TimeSeries(1800, 7, 600, [100 . 200 300 . . .])", metricValues(series, w, timeseries.FillSum)[0].Values.String())
	assert.Equal(t, "TimeSeries(1800, 7, 600, [1000 . 2000 3000 . . .])", metricValues(series, w, timeseries.FillMax)[0].Values.String())

	t.Run("destination_ip is a destination given as an IP", func(t *testing.T) {
		got := metricValues([]*storedSeries{{
			labels: map[string]string{"destination_ip": "10.0.0.5"},
			ts:     []uint32{1800}, last: []float32{1},
		}}, w, timeseries.FillAny)
		require.Len(t, got, 1)
		assert.Equal(t, "10.0.0.5", got[0].Destination)
		assert.True(t, got[0].DestIp)
	})
	t.Run("a NaN point is no point", func(t *testing.T) {
		got := metricValues([]*storedSeries{{
			labels: map[string]string{"app": "a"},
			ts:     []uint32{1800}, last: []float32{timeseries.NaN},
		}}, w, timeseries.FillAny)
		assert.Empty(t, got)
	})
}

func TestAggregateFor(t *testing.T) {
	assert.Equal(t, aggLast, aggregateFor(nil))
	assert.Equal(t, aggLast, aggregateFor(timeseries.FillAny))
	assert.Equal(t, aggAvg, aggregateFor(timeseries.FillAvg))
	assert.Equal(t, aggSum, aggregateFor(timeseries.FillSum))
	assert.Equal(t, aggMax, aggregateFor(timeseries.FillMax))
}

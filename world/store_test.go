package world

import (
	"testing"

	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

func TestPointsSQLPicksTableByStep(t *testing.T) {
	from, to := timeseries.Time(1_790_000_100), timeseries.Time(1_790_003_700)

	// the stored resolution: the points as they are, no bucket and no aggregate
	raw := pointsSQL(from, to, 15)
	assert.Contains(t, raw, "FROM @@table_world_points@@")
	assert.Contains(t, raw, "toStartOfHour(Timestamp) BETWEEN toStartOfHour(toDateTime(1790000100))")
	assert.Contains(t, raw, "Timestamp >= toDateTime(1790000100) AND Timestamp <= toDateTime(1790003700)")
	assert.Contains(t, raw, "AS Ts, groupArray(Value) AS Ls")
	assert.NotContains(t, raw, "argMax")
	assert.NotContains(t, raw, "toStartOfInterval")
	assert.Contains(t, raw, "GROUP BY Query, SeriesHash")

	r5 := pointsSQL(from, to, 15*timeseries.Minute)
	assert.Contains(t, r5, "FROM @@table_world_points_5m@@")
	assert.Contains(t, r5, "argMaxMerge(Last) AS l")
	assert.Contains(t, r5, "sum(Sum)/sum(Cnt) AS a")
	assert.Contains(t, r5, "max(Max) AS m")
	assert.Contains(t, r5, "toStartOfDay(Timestamp) BETWEEN")
	assert.Contains(t, r5, "INTERVAL 900 SECOND) + INTERVAL 900 SECOND")

	assert.Contains(t, pointsSQL(from, to, timeseries.Hour), "FROM @@table_world_points_1h@@")
	assert.Contains(t, pointsSQL(from, to, 2*timeseries.Hour), "FROM @@table_world_points_1h@@")

	// a step that is not a multiple of 5 min buckets the raw points
	r2 := pointsSQL(from, to, 2*timeseries.Minute)
	assert.Contains(t, r2, "FROM @@table_world_points@@")
	assert.Contains(t, r2, "INTERVAL 120 SECOND")

	// bucketed: one row per series, the three aggregates as arrays, right-closed buckets
	assert.Contains(t, r2, "AS Ts, groupArray(toFloat32(l)) AS Ls, groupArray(toFloat32(a)) AS As, groupArray(toFloat32(s)) AS Ss, groupArray(toFloat32(m)) AS Ms")
	assert.Contains(t, r2, "argMax(Value, Timestamp) AS l")
	assert.Contains(t, r2, "max(Value) AS m")
	assert.Contains(t, r2, "toStartOfInterval(Timestamp - INTERVAL 1 SECOND, INTERVAL 120 SECOND) + INTERVAL 120 SECOND")
}

func TestAggregateFor(t *testing.T) {
	assert.Equal(t, aggLast, aggregateFor(nil))
	assert.Equal(t, aggLast, aggregateFor(timeseries.FillAny))
	assert.Equal(t, aggAvg, aggregateFor(timeseries.FillAvg))
	assert.Equal(t, aggSum, aggregateFor(timeseries.FillSum))
	assert.Equal(t, aggMax, aggregateFor(timeseries.FillMax))
}

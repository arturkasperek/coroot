package world

import (
	"fmt"
	"testing"

	"github.com/coroot/coroot/constructor"
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

func TestPointsSQLReadsOnlyThePlannedQueries(t *testing.T) {
	from, to := timeseries.Time(1_790_000_100), timeseries.Time(1_790_003_700)
	assert.NotContains(t, pointsSQL(from, to, 15, nil), "Query IN")
	assert.Contains(t, pointsSQL(from, to, 15, []string{"a", `up{job='x\y'}`}), `AND Query IN ('a', 'up{job=\'x\\y\'}')`)
	assert.Contains(t, pointsSQL(from, to, timeseries.Hour, []string{"a"}), "AND Query IN ('a')")
	assert.NotContains(t, seriesSQL(nil), "WHERE")
	assert.Contains(t, seriesSQL([]string{"a"}), "FINAL WHERE true AND Query IN ('a')")
}

func TestStorePlansWindows(t *testing.T) {
	s := NewStore(nil)
	w := window{from: 900, to: 1800, step: 15}
	sli := []string{"rr_application_l7_requests", "rr_application_l7_histogram"}
	s.PlanQueries(map[constructor.Window][]string{{From: 900, To: 1800, Step: 15}: sli})
	require.Len(t, s.windows[w], 1)
	assert.Equal(t, map[string]bool{"rr_application_l7_requests": true, "rr_application_l7_histogram": true}, s.windows[w][0].queries)

	// a run that asks for the same queries uses the same window
	s.PlanQueries(map[constructor.Window][]string{{From: 900, To: 1800, Step: 15}: sli[:1]})
	require.Len(t, s.windows[w], 1)

	// a query not planned: a window of every query
	s.PlanQueries(map[constructor.Window][]string{{From: 900, To: 1800, Step: 15}: {"up"}})
	require.Len(t, s.windows[w], 2)
	many := make([]string, maxFilteredQueries+1)
	for i := range many {
		many[i] = fmt.Sprint("q", i)
	}
	s.PlanQueries(map[constructor.Window][]string{{From: 900, To: 1800, Step: 15}: many})
	require.Len(t, s.windows[w], 3)
	assert.Nil(t, s.windows[w][2].queries, "too many to list: all of them")
	s.PlanQueries(map[constructor.Window][]string{{From: 900, To: 1800, Step: 15}: {"anything"}})
	assert.Len(t, s.windows[w], 3, "the window of every query has it")
}

func TestAggregateFor(t *testing.T) {
	assert.Equal(t, aggLast, aggregateFor(nil))
	assert.Equal(t, aggLast, aggregateFor(timeseries.FillAny))
	assert.Equal(t, aggAvg, aggregateFor(timeseries.FillAvg))
	assert.Equal(t, aggSum, aggregateFor(timeseries.FillSum))
	assert.Equal(t, aggMax, aggregateFor(timeseries.FillMax))
}

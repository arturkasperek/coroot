// Package world keeps the results of the constructor's queries in ClickHouse,
// so that a page reads them in one query instead of evaluating PromQL.
package world

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
)

// Step is the resolution of the stored points: the agents' scrape interval.
const Step = 15 * timeseries.Second

type aggregate int

const (
	aggLast aggregate = iota // timeseries.FillAny: the last value of the bucket
	aggAvg                   // timeseries.FillAvg
	aggSum                   // timeseries.FillSum
	aggMax                   // timeseries.FillMax
)

// aggregateFor maps the constructor's fill function to the value that gives
// what today's chunk cache gives when it widens 15 s points to a page step.
func aggregateFor(f timeseries.FillFunc) aggregate {
	if f == nil {
		return aggLast
	}
	switch reflect.ValueOf(f).Pointer() {
	case reflect.ValueOf(timeseries.FillAvg).Pointer():
		return aggAvg
	case reflect.ValueOf(timeseries.FillSum).Pointer():
		return aggSum
	case reflect.ValueOf(timeseries.FillMax).Pointer():
		return aggMax
	}
	return aggLast
}

// pointsSQL returns one row per (Query, SeriesHash) with the points of the
// window at the page step. A point stamped T covers (T - step, T], like
// timeseries.FillAny does, but never reaches before from: the first point is
// the sample at from alone, which is what the chunk cache gives (it drops the
// source samples that precede the series). from and to are multiples of step. Each point
// carries the four aggregates (last, average, sum, max), the store picks one per
// query. Steps that are a multiple of 5 min or 1 h read the rollups.
func pointsSQL(from, to timeseries.Time, step timeseries.Duration) string {
	bucket := fmt.Sprintf("toStartOfInterval(Timestamp - INTERVAL 1 SECOND, INTERVAL %[1]d SECOND) + INTERVAL %[1]d SECOND", step)
	lo := from
	window := fmt.Sprintf("Timestamp >= toDateTime(%d) AND Timestamp <= toDateTime(%d)", lo, to)
	if step == Step {
		// the points as they are stored: no bucket, so no GROUP BY over every point (12 times
		// cheaper than bucketing 15 s points into 15 s buckets)
		return fmt.Sprintf(`SELECT Query, SeriesHash, groupArray(toUInt32(Timestamp)) AS Ts, groupArray(Value) AS Ls
FROM @@table_world_points@@
WHERE toStartOfHour(Timestamp) BETWEEN toStartOfHour(toDateTime(%d)) AND toStartOfHour(toDateTime(%d)) AND %s
GROUP BY Query, SeriesHash`, lo, to, window)
	}
	var src, last, avg, sum, max, prune string
	switch {
	case step >= timeseries.Hour && step%timeseries.Hour == 0:
		src, last, avg, sum, max = "@@table_world_points_1h@@", "argMaxMerge(Last)", "sum(Sum)/sum(Cnt)", "sum(Sum)", "max(Max)"
		prune = fmt.Sprintf("toStartOfDay(Timestamp) BETWEEN toStartOfDay(toDateTime(%d)) AND toStartOfDay(toDateTime(%d))", lo, to)
	case step >= 5*timeseries.Minute && step%(5*timeseries.Minute) == 0:
		src, last, avg, sum, max = "@@table_world_points_5m@@", "argMaxMerge(Last)", "sum(Sum)/sum(Cnt)", "sum(Sum)", "max(Max)"
		prune = fmt.Sprintf("toStartOfDay(Timestamp) BETWEEN toStartOfDay(toDateTime(%d)) AND toStartOfDay(toDateTime(%d))", lo, to)
	default:
		src, last, avg, sum, max = "@@table_world_points@@", "argMax(Value, Timestamp)", "avg(Value)", "sum(Value)", "max(Value)"
		prune = fmt.Sprintf("toStartOfHour(Timestamp) BETWEEN toStartOfHour(toDateTime(%d)) AND toStartOfHour(toDateTime(%d))", lo, to)
	}
	return fmt.Sprintf(`SELECT Query, SeriesHash, groupArray(toUInt32(t)) AS Ts, groupArray(toFloat32(l)) AS Ls, groupArray(toFloat32(a)) AS As, groupArray(toFloat32(s)) AS Ss, groupArray(toFloat32(m)) AS Ms
FROM (SELECT Query, SeriesHash, %s AS t, %s AS l, %s AS a, %s AS s, %s AS m FROM %s WHERE %s AND %s GROUP BY Query, SeriesHash, t)
GROUP BY Query, SeriesHash`, bucket, last, avg, sum, max, src, prune, window)
}

const seriesSQL = `SELECT Query, SeriesHash, Labels FROM @@table_world_series@@ FINAL`

// Store reads the results of the constructor's queries; it satisfies
// constructor.Cache.
type Store struct {
	c *clickhouse.Client

	lock    sync.Mutex
	windows map[window]*loadedWindow
}

type window struct {
	from, to timeseries.Time
	step     timeseries.Duration
}

type storedSeries struct {
	labels    map[string]string
	hash      uint64
	ts        []uint32
	last, avg []float32
	sum, max  []float32
}

type loadedWindow struct {
	once    sync.Once
	created time.Time
	series  map[string][]*storedSeries // by query
	err     error
}

// windowTTL is how long a loaded window is reused: the constructor asks for
// ~400 queries of the same window, concurrently, in one run.
const windowTTL = 5 * time.Second

func NewStore(c *clickhouse.Client) *Store {
	return &Store{c: c, windows: map[window]*loadedWindow{}}
}

func (s *Store) GetStep(from, to timeseries.Time) (timeseries.Duration, error) {
	return Step, nil
}

// LastEvaluated is the end of the stored results: the step up to which the
// recording rules are computed (the markers of the rules), or the newest point
// when there are no rules yet. Pages end there.
func (s *Store) LastEvaluated(ctx context.Context) (timeseries.Time, error) {
	var t uint32
	if err := s.c.QueryRow(ctx, "SELECT toUInt32(maxIf(Timestamp, startsWith(Query, 'rr_world_computed_'))) FROM @@table_world_points@@").Scan(&t); err != nil {
		return 0, err
	}
	if t == 0 {
		if err := s.c.QueryRow(ctx, "SELECT toUInt32(max(Timestamp)) FROM @@table_world_points@@").Scan(&t); err != nil {
			return 0, err
		}
	}
	return timeseries.Time(t), nil
}

func (s *Store) QueryRange(ctx context.Context, query string, from, to timeseries.Time, step timeseries.Duration, fillFunc timeseries.FillFunc) ([]*model.MetricValues, error) {
	from, to = from.Truncate(step), to.Truncate(step)
	lw, err := s.load(ctx, window{from, to, step})
	if err != nil {
		return nil, err
	}
	agg := aggregateFor(fillFunc)
	n := int(to.Sub(from)/step) + 1
	var res []*model.MetricValues
	for _, ss := range lw.series[query] {
		vals := ss.last
		switch agg {
		case aggAvg:
			vals = ss.avg
		case aggSum:
			vals = ss.sum
		case aggMax:
			vals = ss.max
		}
		mv := &model.MetricValues{LabelsHash: ss.hash, Labels: model.Labels{}, Values: timeseries.New(from, n, step)}
		for k, v := range ss.labels {
			switch k {
			case model.LabelMachineId:
				mv.MachineID = v
			case model.LabelSystemUuid:
				mv.SystemUUID = v
			case model.LabelContainerId:
				mv.ContainerId = v
			case model.LabelDestination:
				mv.Destination = v
			case model.LabelActualDestination:
				mv.ActualDestination = v
			case model.LabelDestinationIP:
				mv.Destination = v
				mv.DestIp = true
			default:
				mv.Labels[k] = v
			}
		}
		changed := false
		for i, t := range ss.ts {
			if v := vals[i]; !timeseries.IsNaN(v) {
				mv.Values.Set(timeseries.Time(t), v)
				changed = true
			}
		}
		if changed {
			res = append(res, mv)
		}
	}
	return res, nil
}

func (s *Store) load(ctx context.Context, w window) (*loadedWindow, error) {
	s.lock.Lock()
	for k, v := range s.windows {
		if time.Since(v.created) > windowTTL {
			delete(s.windows, k)
		}
	}
	lw := s.windows[w]
	if lw == nil {
		lw = &loadedWindow{created: time.Now()}
		s.windows[w] = lw
	}
	s.lock.Unlock()

	// the first caller's context must not cancel the load the others wait for
	lw.once.Do(func() { lw.series, lw.err = s.read(context.WithoutCancel(ctx), w) })
	return lw, lw.err
}

func (s *Store) read(ctx context.Context, w window) (map[string][]*storedSeries, error) {
	type key struct {
		query string
		hash  uint64
	}
	byKey := map[key]*storedSeries{}
	res := map[string][]*storedSeries{}

	rows, err := s.c.Query(ctx, seriesSQL)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q string
		var h uint64
		var labels map[string]string
		if err := rows.Scan(&q, &h, &labels); err != nil {
			rows.Close()
			return nil, err
		}
		ss := &storedSeries{labels: labels, hash: h}
		byKey[key{q, h}] = ss
		res[q] = append(res[q], ss)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = s.c.Query(ctx, pointsSQL(w.from, w.to, w.step))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var q string
		var h uint64
		var ts []uint32
		var l, a, sum, max []float32
		if w.step == Step { // one value per point: last, average, sum and max are the same
			if err := rows.Scan(&q, &h, &ts, &l); err != nil {
				return nil, err
			}
			a, sum, max = l, l, l
		} else if err := rows.Scan(&q, &h, &ts, &l, &a, &sum, &max); err != nil {
			return nil, err
		}
		if ss := byKey[key{q, h}]; ss != nil {
			ss.ts, ss.last, ss.avg, ss.sum, ss.max = ts, l, a, sum, max
		}
	}
	return res, rows.Err()
}

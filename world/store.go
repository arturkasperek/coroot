// Package world keeps the results of the constructor's queries in ClickHouse,
// so that a page reads them in one query instead of evaluating PromQL.
package world

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/constructor"
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

// pointsSQL returns one row per point of the window at the page step (one row
// per point, not an array per series: clickhouse-go scans arrays element by
// element through reflect, ~2.5x the time of the same points as rows). A point stamped T covers (T - step, T], like
// timeseries.FillAny does, but never reaches before from: the first point is
// the sample at from alone, which is what the chunk cache gives (it drops the
// source samples that precede the series). from and to are multiples of step. Each point
// carries the four aggregates (last, average, sum, max), the store picks one per
// query. Steps that are a multiple of 5 min or 1 h read the rollups. queries
// limits the read to those queries; nil reads all of them.
func pointsSQL(from, to timeseries.Time, step timeseries.Duration, queries []string) string {
	bucket := fmt.Sprintf("toStartOfInterval(Timestamp - INTERVAL 1 SECOND, INTERVAL %[1]d SECOND) + INTERVAL %[1]d SECOND", step)
	lo := from
	window := fmt.Sprintf("Timestamp >= toDateTime(%d) AND Timestamp <= toDateTime(%d)", lo, to) + queryFilter(queries)
	if step == Step {
		// the points as they are stored: no bucket, so no GROUP BY over every point (12 times
		// cheaper than bucketing 15 s points into 15 s buckets)
		return fmt.Sprintf(`SELECT Query, SeriesHash, toUInt32(Timestamp) AS T, Value AS L
FROM @@table_world_points@@
WHERE toStartOfHour(Timestamp) BETWEEN toStartOfHour(toDateTime(%d)) AND toStartOfHour(toDateTime(%d)) AND %s`, lo, to, window)
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
	return fmt.Sprintf(`SELECT Query, SeriesHash, toUInt32(%s) AS T, toFloat32(%s) AS L, toFloat32(%s) AS A, toFloat32(%s) AS S, toFloat32(%s) AS M
FROM %s WHERE %s AND %s
GROUP BY Query, SeriesHash, T`, bucket, last, avg, sum, max, src, prune, window)
}

// seriesSQL returns the labels of the series of queries; nil: of every query.
func seriesSQL(queries []string) string {
	where := ""
	if len(queries) > 0 {
		where = " WHERE true" + queryFilter(queries)
	}
	return `SELECT Query, SeriesHash, Labels FROM @@table_world_series@@ FINAL` + where
}

var sqlStringEscaper = strings.NewReplacer(`\`, `\\`, `'`, `\'`)

// queryFilter is the condition " AND Query IN (...)" for queries, "" for nil.
func queryFilter(queries []string) string {
	if len(queries) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(queries))
	for _, query := range queries {
		quoted = append(quoted, "'"+sqlStringEscaper.Replace(query)+"'")
	}
	return " AND Query IN (" + strings.Join(quoted, ", ") + ")"
}

// maxFilteredQueries: a window of more queries reads all of them. A page's
// own window needs nearly all (~340, too long for one IN list); the windows
// worth filtering are the small ones, such as the raw SLIs a 24 h or 7 d page
// reads at 15 s over its whole range.
const maxFilteredQueries = 50

// Store reads the results of the constructor's queries; it satisfies
// constructor.Cache.
type Store struct {
	c *clickhouse.Client

	lock sync.Mutex
	// lastEvaluated is LastEvaluated as read at lastEvaluatedAt: a page
	// served by an instance that is not the primary asks for it, and so would
	// every request
	lastEvaluated   timeseries.Time
	lastEvaluatedAt time.Time
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

// lastEvaluatedTTL is how long LastEvaluated is reused.
const lastEvaluatedTTL = 5 * time.Second

func NewStore(c *clickhouse.Client) *Store {
	return &Store{c: c}
}

func (s *Store) GetStep(from, to timeseries.Time) (timeseries.Duration, error) {
	return Step, nil
}

// LastEvaluated is the end of the stored results: the step up to which the
// recording rules are computed (the markers of the rules), or the newest point
// when there are no rules yet. Pages end there.
// It is read again at most every lastEvaluatedTTL, and from the last day first (the
// partitions of the points are days), the whole table only when that day has
// nothing.
func (s *Store) LastEvaluated(ctx context.Context) (timeseries.Time, error) {
	s.lock.Lock()
	if time.Since(s.lastEvaluatedAt) < lastEvaluatedTTL {
		defer s.lock.Unlock()
		return s.lastEvaluated, nil
	}
	s.lock.Unlock()

	var t uint32
	for _, where := range []string{"WHERE Timestamp > now() - INTERVAL 1 DAY", ""} {
		if err := s.c.QueryRow(ctx, "SELECT toUInt32(maxIf(Timestamp, startsWith(Query, 'rr_world_computed_'))) FROM @@table_world_points@@ "+where).Scan(&t); err != nil {
			return 0, err
		}
		if t == 0 {
			if err := s.c.QueryRow(ctx, "SELECT toUInt32(max(Timestamp)) FROM @@table_world_points@@ "+where).Scan(&t); err != nil {
				return 0, err
			}
		}
		if t != 0 {
			break
		}
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	s.lastEvaluated, s.lastEvaluatedAt = timeseries.Time(t), time.Now()
	return s.lastEvaluated, nil
}

// QueryRanges reads the requests with one read per window, the windows in
// parallel: a window of at most maxFilteredQueries queries reads only those, a
// larger one every query. Nothing is kept after it returns.
func (s *Store) QueryRanges(ctx context.Context, requests map[string]constructor.QueryRequest) (map[string][]*model.MetricValues, error) {
	queriesByWindow := map[window]map[string]bool{}
	for _, request := range requests {
		w := windowOf(request)
		if queriesByWindow[w] == nil {
			queriesByWindow[w] = map[string]bool{}
		}
		queriesByWindow[w][request.Query] = true
	}

	seriesByWindow := make(map[window]map[string][]*storedSeries, len(queriesByWindow))
	var lock sync.Mutex
	var lastErr error
	var wg sync.WaitGroup
	for w, queries := range queriesByWindow {
		wg.Go(func() {
			series, err := s.read(ctx, w, queries)
			lock.Lock()
			defer lock.Unlock()
			if err != nil {
				lastErr = err
				return
			}
			seriesByWindow[w] = series
		})
	}
	wg.Wait()

	res := make(map[string][]*model.MetricValues, len(requests))
	for name, request := range requests {
		w := windowOf(request)
		if series, ok := seriesByWindow[w]; ok {
			res[name] = metricValues(series[request.Query], w, request.FillFunc)
		}
	}
	return res, lastErr
}

// windowOf is the window of a request, aligned to its step.
func windowOf(request constructor.QueryRequest) window {
	return window{request.From.Truncate(request.Step), request.To.Truncate(request.Step), request.Step}
}

// metricValues makes the constructor's series of a query from the series read
// for its window, with the aggregate of fillFunc; a series with no point in the
// window is left out.
func metricValues(series []*storedSeries, w window, fillFunc timeseries.FillFunc) []*model.MetricValues {
	from, step := w.from, w.step
	n := int(w.to.Sub(from)/step) + 1
	agg := aggregateFor(fillFunc)
	var res []*model.MetricValues
	for _, ss := range series {
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
	return res
}

func (s *Store) read(ctx context.Context, w window, queries map[string]bool) (map[string][]*storedSeries, error) {
	var names []string // none: every query
	if len(queries) <= maxFilteredQueries {
		for query := range queries {
			names = append(names, query)
		}
		sort.Strings(names)
	}

	type key struct {
		query string
		hash  uint64
	}
	byKey := map[key]*storedSeries{}
	res := map[string][]*storedSeries{}

	rows, err := s.c.Query(ctx, seriesSQL(names))
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

	rows, err = s.c.Query(ctx, pointsSQL(w.from, w.to, w.step, names))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	raw := w.step == Step // one value per point: last, average, sum and max are the same
	var q string
	var h uint64
	var t uint32
	var l, a, sum, max float32
	for rows.Next() {
		if raw {
			err = rows.Scan(&q, &h, &t, &l)
			a, sum, max = l, l, l
		} else {
			err = rows.Scan(&q, &h, &t, &l, &a, &sum, &max)
		}
		if err != nil {
			return nil, err
		}
		if ss := byKey[key{q, h}]; ss != nil {
			ss.ts = append(ss.ts, t)
			ss.last = append(ss.last, l)
			if !raw {
				ss.avg, ss.sum, ss.max = append(ss.avg, a), append(ss.sum, sum), append(ss.max, max)
			}
		}
	}
	if raw {
		for _, ss := range byKey {
			ss.avg, ss.sum, ss.max = ss.last, ss.last, ss.last
		}
	}
	return res, rows.Err()
}

package world

import (
	"context"
	"fmt"
	"sync"

	"github.com/coroot/coroot/constructor"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/promql"
	"github.com/coroot/coroot/timeseries"
	"k8s.io/klog"
)

const (
	// Backfill is how far back a query that has no stored points is evaluated (in one call).
	Backfill = 24 * timeseries.Hour
	// seriesRefresh: the labels of a series are written again after this long, so that
	// LastSeen (and the TTL of the series row) moves on while the series is alive.
	seriesRefresh = 10 * timeseries.Minute
	concurrency   = 8
	// a step is final two steps after it: the agents' remote write and the collector's
	// batches need time to land (today's cache lags the same way)
	lag = 2
)

type seriesRow struct {
	query    string
	hash     uint64
	labels   map[string]string
	lastSeen timeseries.Time
}

type pointRow struct {
	query string
	hash  uint64
	t     timeseries.Time
	v     float32
}

type batch struct {
	series []seriesRow
	points []pointRow
}

// backend is what a cycle needs from ClickHouse; the tests fake it.
type backend interface {
	metricNames(ctx context.Context) (map[string]bool, error)
	lastPerQuery(ctx context.Context) (map[string]timeseries.Time, error)
	queryRange(ctx context.Context, q constructor.Query, from, to timeseries.Time, step timeseries.Duration) ([]*model.MetricValues, error)
	write(ctx context.Context, b batch) error
}

type seriesKey struct {
	query string
	hash  uint64
}

// seriesCache remembers when the labels of a series were last written.
type seriesCache struct {
	lock sync.Mutex
	m    map[seriesKey]timeseries.Time
}

func newSeriesCache() *seriesCache { return &seriesCache{m: map[seriesKey]timeseries.Time{}} }

// due says whether the labels of the series must be written, and counts them as written.
func (c *seriesCache) due(query string, hash uint64, now timeseries.Time) bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	k := seriesKey{query, hash}
	if at, ok := c.m[k]; ok && now.Sub(at) < seriesRefresh {
		return false
	}
	c.m[k] = now
	return true
}

// Result of one cycle for a project.
type Result struct {
	To               timeseries.Time
	Queries, Skipped int
	Series, Points   int
	FirstError       error
}

// labelMap is the labels of a series as they are stored: the fields the
// constructor reads directly (machine id, container id, destination...) go back
// into the map, Store.QueryRange splits them out again.
func labelMap(mv *model.MetricValues) map[string]string {
	ls := make(map[string]string, len(mv.Labels)+6)
	for k, v := range mv.Labels {
		ls[k] = v
	}
	set := func(k, v string) {
		if v != "" {
			ls[k] = v
		}
	}
	set(model.LabelMachineId, mv.MachineID)
	set(model.LabelSystemUuid, mv.SystemUUID)
	set(model.LabelContainerId, mv.ContainerId)
	if mv.DestIp {
		set(model.LabelDestinationIP, mv.Destination)
	} else {
		set(model.LabelDestination, mv.Destination)
	}
	set(model.LabelActualDestination, mv.ActualDestination)
	return ls
}

// cycle evaluates the queries over the steps they are missing (all of them in one
// call per query) and stores the results. It evaluates only queries whose metrics
// exist: three quarters of the constructor's queries read metrics that nobody
// sends, and ClickHouse charges a fixed ~14 CPU-ms per PromQL query.
func cycle(ctx context.Context, b backend, queries []constructor.Query, now timeseries.Time, backfill timeseries.Duration, seen *seriesCache) (Result, error) {
	to := now.Add(-lag * Step).Truncate(Step)
	res := Result{To: to}

	names, err := b.metricNames(ctx)
	if err != nil {
		return res, fmt.Errorf("metric names: %w", err)
	}
	last, err := b.lastPerQuery(ctx)
	if err != nil {
		return res, fmt.Errorf("last evaluated: %w", err)
	}

	type task struct {
		q    constructor.Query
		from timeseries.Time
	}
	var tasks []task
	done := map[string]bool{}
	for _, q := range queries {
		if q.Query == "" || done[q.Query] {
			continue
		}
		done[q.Query] = true
		selectors, err := promql.Selectors(q.Query)
		if err != nil {
			klog.Warningf("skipping query %q: %v", q.Query, err)
			res.Skipped++
			continue
		}
		if selectors != nil && !anyKnown(selectors, names) {
			res.Skipped++
			continue
		}
		from := to.Add(-backfill)
		if l, ok := last[q.Query]; ok && l.Add(Step) > from {
			from = l.Add(Step)
		}
		if from > to {
			continue
		}
		tasks = append(tasks, task{q, from})
	}
	res.Queries = len(tasks)

	var lock sync.Mutex
	var out batch
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(t task) {
			defer wg.Done()
			defer func() { <-sem }()
			mvs, err := b.queryRange(ctx, t.q, t.from, to, Step)
			lock.Lock()
			defer lock.Unlock()
			if err != nil {
				klog.Errorf("failed to evaluate %q: %v", t.q.Query, err)
				if res.FirstError == nil {
					res.FirstError = err
				}
				return
			}
			for _, mv := range mvs {
				appendSeries(&out, seen, t.q.Query, mv, to, 0, 0)
			}
		}(t)
	}
	wg.Wait()

	res.Series, res.Points = len(out.series), len(out.points)
	if len(out.points) > 0 {
		if err := b.write(ctx, out); err != nil {
			return res, fmt.Errorf("write: %w", err)
		}
	}
	return res, nil
}

func anyKnown(selectors []string, names map[string]bool) bool {
	for _, s := range selectors {
		if names[s] {
			return true
		}
	}
	return false
}

// appendSeries adds the points of a series that are not before notBefore and not
// after notAfter (zero: no limit).
func appendSeries(out *batch, seen *seriesCache, query string, mv *model.MetricValues, now, notBefore, notAfter timeseries.Time) {
	n := len(out.points)
	it := mv.Values.Iter()
	for it.Next() {
		t, v := it.Value()
		if timeseries.IsNaN(v) || t < notBefore || (notAfter != 0 && t > notAfter) {
			continue
		}
		out.points = append(out.points, pointRow{query, mv.LabelsHash, t, v})
	}
	if len(out.points) == n {
		return
	}
	if seen.due(query, mv.LabelsHash, now) {
		out.series = append(out.series, seriesRow{query, mv.LabelsHash, labelMap(mv), now})
	}
}

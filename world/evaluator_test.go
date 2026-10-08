package world

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"testing"

	"github.com/coroot/coroot/constructor"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBackend struct {
	lock    sync.Mutex
	names   map[string]bool
	last    map[string]timeseries.Time
	results map[string][]*model.MetricValues
	calls   []string
	written []written
	series  []seriesRow
}

type written struct {
	query string
	hash  uint64
	t     timeseries.Time
	v     float32
}

func (f *fakeBackend) metricNames(context.Context) (map[string]bool, error) { return f.names, nil }
func (f *fakeBackend) lastPerQuery(context.Context) (map[string]timeseries.Time, error) {
	return f.last, nil
}
func (f *fakeBackend) queryRange(_ context.Context, q constructor.Query, from, to timeseries.Time, step timeseries.Duration) ([]*model.MetricValues, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%s|%d|%d", q.Query, from, to))
	return f.results[q.Query], nil
}
func (f *fakeBackend) write(_ context.Context, b batch) error {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.series = append(f.series, b.series...)
	for _, p := range b.points {
		f.written = append(f.written, written{p.query, p.hash, p.t, p.v})
	}
	return nil
}

func q(expr string, labels ...string) constructor.Query {
	return constructor.Q("", expr, labels...)
}

func TestCycleEvaluatesOnlyExistingMetricsAndOnlyNewSteps(t *testing.T) {
	f := &fakeBackend{names: map[string]bool{"up": true}, last: map[string]timeseries.Time{"up": 1020}}
	// now 1095: the window ends two steps back, at 1065
	_, err := cycle(context.Background(), f, []constructor.Query{q("up"), q("pg_up")}, 1095, 24*timeseries.Hour, newSeriesCache())
	require.NoError(t, err)
	assert.Equal(t, []string{"up|1035|1065"}, f.calls, "pg_up has no series; up is evaluated after its last point only")
}

func TestCycleBackfillsAQueryWithNoPoints(t *testing.T) {
	f := &fakeBackend{names: map[string]bool{"pg_up": true}, last: map[string]timeseries.Time{}}
	_, err := cycle(context.Background(), f, []constructor.Query{q("pg_up")}, 100_080, 24*timeseries.Hour, newSeriesCache())
	require.NoError(t, err)
	assert.Equal(t, []string{"pg_up|13650|100050"}, f.calls, "a new query is evaluated over the backfill window in one call")
}

func TestCycleEvaluatesQueriesWhoseMetricsItCannotTell(t *testing.T) {
	f := &fakeBackend{names: map[string]bool{}, last: map[string]timeseries.Time{}}
	_, err := cycle(context.Background(), f, []constructor.Query{q(`{job="x"}`), q(`rate(`)}, 100_080, timeseries.Hour, newSeriesCache())
	require.NoError(t, err)
	assert.Equal(t, []string{`{job="x"}|96450|100050`}, f.calls, "no metric name: evaluated; a query that does not parse is skipped")
}

func TestCycleSkipsAQueryThatIsUpToDate(t *testing.T) {
	f := &fakeBackend{names: map[string]bool{"up": true}, last: map[string]timeseries.Time{"up": 1065}}
	_, err := cycle(context.Background(), f, []constructor.Query{q("up")}, 1095, timeseries.Hour, newSeriesCache())
	require.NoError(t, err)
	assert.Empty(t, f.calls)
}

func mv(labels map[string]string, vals ...float32) *model.MetricValues {
	ts := timeseries.New(1035, len(vals), 15)
	for i, v := range vals {
		ts.Set(timeseries.Time(1035+15*i), v)
	}
	return &model.MetricValues{Labels: labels, LabelsHash: model.Labels(labels).Hash(), Values: ts}
}

func TestCycleDropsNaNAndWritesSeriesOnce(t *testing.T) {
	nan := float32(math.NaN())
	f := &fakeBackend{names: map[string]bool{"up": true}, last: map[string]timeseries.Time{"up": 1020},
		results: map[string][]*model.MetricValues{"up": {mv(map[string]string{"job": "a"}, 1, nan, 3)}}}
	seen := newSeriesCache()
	res, err := cycle(context.Background(), f, []constructor.Query{q("up")}, 1095, timeseries.Hour, seen)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Points)
	sort.Slice(f.written, func(i, j int) bool { return f.written[i].t < f.written[j].t })
	require.Len(t, f.written, 2)
	assert.Equal(t, timeseries.Time(1035), f.written[0].t)
	assert.Equal(t, timeseries.Time(1065), f.written[1].t, "the NaN point at 1050 is not written")
	require.Len(t, f.series, 1)
	assert.Equal(t, map[string]string{"job": "a"}, f.series[0].labels)

	// the next cycle, one step later, does not write the series again
	f.series, f.written, f.calls = nil, nil, nil
	f.last["up"] = 1065
	_, err = cycle(context.Background(), f, []constructor.Query{q("up")}, 1125, timeseries.Hour, seen)
	require.NoError(t, err)
	assert.Empty(t, f.series, "a series is rewritten only when it is new or old")
}

func TestLabelMapPutsTheSpecialFieldsBackIntoTheLabels(t *testing.T) {
	m := &model.MetricValues{
		Labels:          model.Labels{"app": "a"},
		NodeContainerId: model.NodeContainerId{NodeId: model.NodeId{MachineID: "m1", SystemUUID: "u1"}, ContainerId: "/k8s/ns/p/c"},
		ConnectionKey:   model.ConnectionKey{Destination: "10.0.0.1:80", ActualDestination: "10.0.0.2:80"},
	}
	assert.Equal(t, map[string]string{
		"app": "a", model.LabelMachineId: "m1", model.LabelSystemUuid: "u1", model.LabelContainerId: "/k8s/ns/p/c",
		model.LabelDestination: "10.0.0.1:80", model.LabelActualDestination: "10.0.0.2:80",
	}, labelMap(m))
	m.DestIp = true
	got := labelMap(m)
	assert.Equal(t, "10.0.0.1:80", got[model.LabelDestinationIP])
	_, hasDestination := got[model.LabelDestination]
	assert.False(t, hasDestination)
}

var _ = utils.NewStringSet

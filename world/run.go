package world

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/constructor"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/promql"
	"github.com/coroot/coroot/timeseries"
	"k8s.io/klog"
)

// Period is how often the evaluator runs.
const Period = 15 * time.Second

// Evaluator keeps the world tables of every project up to date.
type Evaluator struct {
	db               *db.DB
	globalClickHouse *db.IntegrationClickhouse
	period           time.Duration
	backfill         timeseries.Duration

	lock     sync.Mutex
	projects map[db.ProjectId]*projectState
	status   map[db.ProjectId]evalStatus
	updates  chan db.ProjectId
}

type projectState struct {
	ll    *ch.LowLevelClient
	c     *clickhouse.Client
	b     *chBackend
	store *Store
	seen  *seriesCache
}

func NewEvaluator(database *db.DB, globalClickHouse *db.IntegrationClickhouse, period time.Duration, backfill timeseries.Duration) *Evaluator {
	return &Evaluator{
		db: database, globalClickHouse: globalClickHouse, period: period, backfill: backfill,
		projects: map[db.ProjectId]*projectState{},
		status:   map[db.ProjectId]evalStatus{},
		updates:  make(chan db.ProjectId, 1),
	}
}

// Updates gets a project id after every cycle that stored points.
func (e *Evaluator) Updates() <-chan db.ProjectId { return e.updates }

// evalStatus is the outcome of the last cycle of a project.
type evalStatus struct {
	lastEvaluated timeseries.Time
	err           string
	duration      time.Duration
}

func (e *Evaluator) state(project *db.Project) (*projectState, error) {
	e.lock.Lock()
	ps := e.projects[project.Id]
	e.lock.Unlock()
	if ps != nil {
		return ps, nil
	}
	cfg := project.ClickHouseConfig(e.globalClickHouse)
	if cfg == nil {
		return nil, fmt.Errorf("project %s has no ClickHouse", project.Id)
	}
	ll, err := ch.NewLowLevelClient(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	c, err := clickhouse.NewClient(cfg, project)
	if err != nil {
		ll.Close()
		return nil, err
	}
	ps = &projectState{
		ll: ll, c: c,
		b:     &chBackend{ll: ll, c: c, prom: promql.New(c, Step)},
		store: NewStore(c),
		seen:  newSeriesCache(),
	}
	e.lock.Lock()
	e.projects[project.Id] = ps
	e.lock.Unlock()
	return ps, nil
}

func (e *Evaluator) drop(id db.ProjectId) {
	e.lock.Lock()
	ps := e.projects[id]
	delete(e.projects, id)
	e.lock.Unlock()
	if ps != nil {
		ps.ll.Close()
		_ = ps.c.Close()
	}
}

// EvaluateProject runs one cycle for the project as of now.
func (e *Evaluator) EvaluateProject(ctx context.Context, project *db.Project, now timeseries.Time) (Result, error) {
	ps, err := e.state(project)
	if err != nil {
		return Result{}, err
	}
	checkConfigs, err := e.db.GetCheckConfigs(project.Id)
	if err != nil {
		return Result{}, fmt.Errorf("could not get check configs: %w", err)
	}
	queries := constructor.EvaluatedQueries(project, checkConfigs)
	res, err := cycle(ctx, ps.b, queries, now, e.backfill, ps.seen)
	if err != nil {
		e.drop(project.Id) // new connections next time
		return res, err
	}
	// the recording rules are computed from the World the stored results make
	if err := e.recordingRules(ctx, project, ps, res.To, &res); err != nil {
		return res, fmt.Errorf("recording rules: %w", err)
	}
	return res, res.FirstError
}

// Run evaluates every project once per period, aligned to the clock, until ctx is done.
func (e *Evaluator) Run(ctx context.Context) {
	for {
		wait := e.period - time.Duration(time.Now().UnixNano())%e.period
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		// with several instances of Coroot only the primary one evaluates (the results are
		// shared: every instance reads them); two would write every point twice
		if !e.db.GetPrimaryLock(ctx) {
			continue
		}
		projects, err := e.db.GetProjects()
		if err != nil {
			klog.Errorln("failed to get projects:", err)
			continue
		}
		for _, project := range projects {
			if project.Multicluster() {
				continue
			}
			e.runProject(ctx, project)
		}
	}
}

func (e *Evaluator) runProject(ctx context.Context, project *db.Project) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := e.EvaluateProject(ctx, project, timeseries.Now())
	st := evalStatus{duration: time.Since(start), lastEvaluated: res.To}
	if err != nil {
		st.err = err.Error()
		klog.Errorf("%s: world evaluation failed: %v", project.Id, err)
	} else {
		klog.Infof("%s: evaluated %d queries (%d skipped), %d series, %d points in %s", project.Id, res.Queries, res.Skipped, res.Series, res.Points, st.duration.Truncate(time.Millisecond))
	}
	e.lock.Lock()
	if err != nil && st.lastEvaluated == 0 { // a failed cycle does not take back what an earlier one stored
		st.lastEvaluated = e.status[project.Id].lastEvaluated
	}
	e.status[project.Id] = st
	e.lock.Unlock()
	if res.Points > 0 {
		select {
		case e.updates <- project.Id:
		default:
		}
	}
}

// Close releases the ClickHouse connections.
func (e *Evaluator) Close() {
	e.lock.Lock()
	ids := make([]db.ProjectId, 0, len(e.projects))
	for id := range e.projects {
		ids = append(ids, id)
	}
	e.lock.Unlock()
	for _, id := range ids {
		e.drop(id)
	}
}

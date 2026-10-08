package world

import (
	"context"
	"fmt"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/promql"
	"github.com/coroot/coroot/timeseries"
)

// Status of the stored results of a project, for the UI.
type Status struct {
	Error  string
	LagMax timeseries.Duration
	LagAvg timeseries.Duration
}

// Client is the world of one project: what the constructor reads (it satisfies
// constructor.Cache) and what the pages need to know about its freshness.
type Client struct {
	e  *Evaluator
	id db.ProjectId
}

// Client of a project; it connects to the project's ClickHouse when first used.
func (e *Evaluator) Client(id db.ProjectId) *Client { return &Client{e: e, id: id} }

func (c *Client) store() (*Store, error) {
	if st := c.e.cachedStore(c.id); st != nil {
		return st, nil
	}
	project, err := c.e.db.GetProject(c.id)
	if err != nil {
		return nil, fmt.Errorf("project %s: %w", c.id, err)
	}
	ps, err := c.e.state(project)
	if err != nil {
		return nil, err
	}
	return ps.store, nil
}

func (c *Client) QueryRange(ctx context.Context, query string, from, to timeseries.Time, step timeseries.Duration, fillFunc timeseries.FillFunc) ([]*model.MetricValues, error) {
	st, err := c.store()
	if err != nil {
		return nil, err
	}
	return st.QueryRange(ctx, query, from, to, step, fillFunc)
}

func (c *Client) GetStep(from, to timeseries.Time) (timeseries.Duration, error) { return Step, nil }

// GetTo is the end of the stored results: a page ends there. Zero when there is nothing yet.
func (c *Client) GetTo() (timeseries.Time, error) {
	if t := c.e.lastEvaluated(c.id); t != 0 {
		return t, nil
	}
	st, err := c.store()
	if err != nil {
		return 0, err
	}
	return st.LastEvaluated(context.Background())
}

func (c *Client) GetStatus() (*Status, error) {
	to, err := c.GetTo()
	if err != nil {
		return nil, err
	}
	s := &Status{Error: c.e.lastError(c.id)}
	if to == 0 {
		s.LagMax, s.LagAvg = Backfill, Backfill
		return s, nil
	}
	lag := timeseries.Now().Sub(to)
	s.LagMax, s.LagAvg = lag, lag
	return s, nil
}

// PromQL is a PromQL client for the project, on the evaluator's ClickHouse connection.
func (e *Evaluator) PromQL(project *db.Project) (*promql.Client, error) {
	ps, err := e.state(project)
	if err != nil {
		return nil, err
	}
	return promql.New(ps.c, Step), nil
}

func (e *Evaluator) cachedStore(id db.ProjectId) *Store {
	e.lock.Lock()
	defer e.lock.Unlock()
	if ps := e.projects[id]; ps != nil {
		return ps.store
	}
	return nil
}

func (e *Evaluator) lastEvaluated(id db.ProjectId) timeseries.Time {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.status[id].lastEvaluated
}

func (e *Evaluator) lastError(id db.ProjectId) string {
	e.lock.Lock()
	defer e.lock.Unlock()
	return e.status[id].err
}

package world

import (
	"testing"
	"time"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

// With several Coroot replicas only the primary evaluates, and only it knows
// in memory up to when the results are stored. A replica that stopped being the
// primary must not keep serving the time of its last evaluation: past two
// periods it is not trusted, and the store is asked.
func TestRecentlyEvaluated(t *testing.T) {
	e := &Evaluator{period: 15 * time.Second, status: map[db.ProjectId]evalStatus{}}
	now := time.Now()

	_, ok := e.recentlyEvaluated("p", now)
	assert.False(t, ok, "never evaluated here: not the primary")

	e.status["p"] = evalStatus{lastEvaluated: timeseries.Time(1000), at: now.Add(-20 * time.Second)}
	to, ok := e.recentlyEvaluated("p", now)
	assert.True(t, ok)
	assert.Equal(t, timeseries.Time(1000), to)

	_, ok = e.recentlyEvaluated("p", now.Add(time.Minute))
	assert.False(t, ok, "no longer the primary: stale")
}

//go:build e2e

package storage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two Coroot replicas share one Postgres: one of them is the primary (it runs
// the evaluator, the watchers, the notifications) and stays so; the other
// never becomes it while the primary lives, however often both ask, from
// several goroutines at once. When the primary goes away, the other takes over.
func TestPrimaryLockHasOneStableHolder(t *testing.T) {
	ctx := context.Background()
	first, dsn := dbtest.NewWithDSN(t)
	second := dbtest.Open(t, dsn)

	require.True(t, first.GetPrimaryLock(ctx))
	for round := 0; round < 50; round++ {
		var wait sync.WaitGroup
		results := make([]bool, 8)
		for index := range results {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if index%2 == 0 {
					results[index] = first.GetPrimaryLock(ctx)
				} else {
					results[index] = second.GetPrimaryLock(ctx)
				}
			}()
		}
		wait.Wait()
		for index, primary := range results {
			assert.Equal(t, index%2 == 0, primary, "round %d: the first stays the primary", round)
		}
	}

	require.NoError(t, first.Close()) // the primary replica stops
	assert.True(t, second.GetPrimaryLock(ctx), "the other one takes over")
	assert.True(t, second.GetPrimaryLock(ctx))
}

// Two Coroot replicas that start at once migrate the database one after the
// other: a migration of one waits while the other's runs.
func TestMigrationsOfTwoReplicasDoNotOverlap(t *testing.T) {
	one, dsn := dbtest.NewWithDSN(t)
	other := dbtest.Open(t, dsn)

	var running, overlaps atomic.Int64
	section := func() error {
		if running.Add(1) > 1 {
			overlaps.Add(1)
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return nil
	}
	var wait sync.WaitGroup
	for index := 0; index < 6; index++ {
		database := one
		if index%2 == 1 {
			database = other
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			assert.NoError(t, database.WithLock(context.Background(), db.MigrationLockId, section))
		}()
	}
	wait.Wait()
	assert.Zero(t, overlaps.Load())
	require.NoError(t, other.Migrate(), "the schema migrates under the lock")
}

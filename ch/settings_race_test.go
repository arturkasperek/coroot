package ch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// The evaluator runs its queries in parallel from one context that carries
// settings and a deadline; clickhouse-go writes max_execution_time into the
// settings of the context it derives from, so they must not be shared.
func TestSettingsAreSafeForConcurrentQueries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	parent := WithSettings(ctx, clickhouse.Settings{"log_comment": "coroot-evaluator"})
	var wait sync.WaitGroup
	for range 50 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				_ = QueryContext(TimeSeriesContext(parent))
			}
		}()
	}
	wait.Wait()
}

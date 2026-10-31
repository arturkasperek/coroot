package constructor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryFillFuncs(t *testing.T) {
	queries := map[string]Query{}
	for _, q := range QUERIES {
		queries[q.Name] = q
	}
	fillFuncName := func(name string) string {
		q, ok := queries[name]
		require.True(t, ok, name)
		switch reflect.ValueOf(q.FillFunc).Pointer() {
		case 0:
			return "last"
		case reflect.ValueOf(timeseries.FillAvg).Pointer():
			return "avg"
		case reflect.ValueOf(timeseries.FillMax).Pointer():
			return "max"
		case reflect.ValueOf(timeseries.FillSum).Pointer():
			return "sum"
		}
		return "other"
	}

	t.Run("a rate is the average of a step", func(t *testing.T) {
		assert.Equal(t, "avg", fillFuncName("node_net_rx_bytes"))
		assert.Equal(t, "avg", fillFuncName("container_http_requests_count"))
		assert.Equal(t, "avg", fillFuncName("mongo_opcounters_total"))
		for _, q := range QUERIES {
			if strings.Contains(q.Query, "rate(") && q.Name != "container_cpu_usage" {
				assert.Equal(t, "avg", fillFuncName(q.Name), q.Name)
			}
		}
	})
	t.Run("a rate computed by the agent too", func(t *testing.T) {
		assert.Equal(t, "avg", fillFuncName("pg_db_queries_per_second"))
		assert.Equal(t, "avg", fillFuncName("aws_rds_net_rx_bytes_per_second"))
	})
	t.Run("the peak of a step where the peak matters", func(t *testing.T) {
		assert.Equal(t, "max", fillFuncName("container_cpu_usage"))
		assert.Equal(t, "max", fillFuncName("container_memory_rss"))
		assert.Equal(t, "max", fillFuncName("pg_connections"))
		assert.Equal(t, "max", fillFuncName("mysql_replication_lag_seconds"))
		assert.Equal(t, "max", fillFuncName("container_volume_used"))
	})
	t.Run("the last value of a counter, as Increase needs it", func(t *testing.T) {
		for _, name := range []string{"container_restarts", "container_oom_kills_total", "container_log_messages", "pg_checkpoints_total", "pg_wal_archived_segments_total"} {
			assert.Equal(t, "last", fillFuncName(name), name)
		}
	})
	t.Run("the last value of a state", func(t *testing.T) {
		assert.Equal(t, "last", fillFuncName("up"))
		assert.Equal(t, "last", fillFuncName("kube_pod_status_phase"))
		assert.Equal(t, "last", fillFuncName("container_memory_limit"))
	})
}

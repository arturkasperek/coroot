package world

import (
	"context"
	"time"

	chgo "github.com/ClickHouse/ch-go"
	chproto "github.com/ClickHouse/ch-go/proto"
	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/constructor"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/promql"
	"github.com/coroot/coroot/timeseries"
)

// foreground inserts return when the shard has the rows (and the rollups, which
// are materialized views on the shard, have them too): a page that follows the
// notification of a cycle must find them.
var foreground = []chgo.Setting{chgo.SettingInt("distributed_foreground_insert", 1)}

type chBackend struct {
	ll   *ch.LowLevelClient
	c    *clickhouse.Client
	prom *promql.Client
}

func (b *chBackend) metricNames(ctx context.Context) (map[string]bool, error) {
	return b.prom.MetricNames(ctx)
}

func (b *chBackend) lastPerQuery(ctx context.Context) (map[string]timeseries.Time, error) {
	rows, err := b.c.Query(ctx, "SELECT Query, toUInt32(max(Timestamp)) FROM @@table_world_points@@ GROUP BY Query")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := map[string]timeseries.Time{}
	for rows.Next() {
		var q string
		var t uint32
		if err := rows.Scan(&q, &t); err != nil {
			return nil, err
		}
		res[q] = timeseries.Time(t)
	}
	return res, rows.Err()
}

func (b *chBackend) queryRange(ctx context.Context, q constructor.Query, from, to timeseries.Time, step timeseries.Duration) ([]*model.MetricValues, error) {
	return b.prom.QueryRange(ctx, q.Query, q.Labels.Has, from, to, step)
}

func (b *chBackend) write(ctx context.Context, bt batch) error {
	if len(bt.series) > 0 {
		query := new(chproto.ColStr).LowCardinality()
		hash := new(chproto.ColUInt64)
		labels := chproto.NewMap[string, string](new(chproto.ColStr).LowCardinality(), new(chproto.ColStr))
		lastSeen := new(chproto.ColDateTime)
		for _, s := range bt.series {
			query.Append(s.query)
			hash.Append(s.hash)
			labels.Append(s.labels)
			lastSeen.Append(time.Unix(int64(s.lastSeen), 0))
		}
		// the series go first, so that points are never read without their labels
		input := chproto.Input{
			{Name: "Query", Data: query}, {Name: "SeriesHash", Data: hash},
			{Name: "Labels", Data: labels}, {Name: "LastSeen", Data: lastSeen},
		}
		if err := b.ll.Do(ctx, chgo.Query{Body: input.Into("@@table_world_series@@"), Input: input, Settings: foreground}); err != nil {
			return err
		}
	}
	query := new(chproto.ColStr).LowCardinality()
	hash := new(chproto.ColUInt64)
	ts := new(chproto.ColDateTime)
	value := new(chproto.ColFloat32)
	for _, p := range bt.points {
		query.Append(p.query)
		hash.Append(p.hash)
		ts.Append(time.Unix(int64(p.t), 0))
		value.Append(p.v)
	}
	input := chproto.Input{
		{Name: "Query", Data: query}, {Name: "SeriesHash", Data: hash},
		{Name: "Timestamp", Data: ts}, {Name: "Value", Data: value},
	}
	return b.ll.Do(ctx, chgo.Query{Body: input.Into("@@table_world_points@@"), Input: input, Settings: foreground})
}

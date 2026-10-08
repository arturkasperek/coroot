package ch

import (
	"context"

	chgo "github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/clickhouse-go/v2"
)

// The TimeSeries table engine and the timeSeries* functions behind PromQL are
// a private preview in ClickHouse 26.9: every query that touches them needs
// these settings.
var TimeSeriesSettings = []chgo.Setting{
	chgo.SettingInt("enable_time_series_table", 1),
	chgo.SettingInt("enable_time_series_aggregate_functions", 1),
}

// TimeSeriesContext is the same for clickhouse-go queries.
func TimeSeriesContext(ctx context.Context) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"enable_time_series_table":               1,
		"enable_time_series_aggregate_functions": 1,
	}))
}

package ch

import (
	"context"
	"maps"

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
	return WithSettings(ctx, clickhouse.Settings{
		"enable_time_series_table":               1,
		"enable_time_series_aggregate_functions": 1,
	})
}

type settingsKey struct{}

// WithSettings adds settings to the clickhouse-go queries made with ctx and
// keeps the ones added before by WithSettings: clickhouse-go's own
// WithSettings replaces them.
func WithSettings(ctx context.Context, settings clickhouse.Settings) context.Context {
	merged := clickhouse.Settings{}
	if previous, ok := ctx.Value(settingsKey{}).(clickhouse.Settings); ok {
		for key, value := range previous {
			merged[key] = value
		}
	}
	for key, value := range settings {
		merged[key] = value
	}
	ctx = context.WithValue(ctx, settingsKey{}, merged)
	// a copy: clickhouse-go writes max_execution_time into the map it is given
	return clickhouse.Context(ctx, clickhouse.WithSettings(maps.Clone(merged)))
}

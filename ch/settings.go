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
// keeps the ones added before by WithSettings (clickhouse-go's own
// WithSettings replaces them). They reach clickhouse-go in QueryContext.
func WithSettings(ctx context.Context, settings clickhouse.Settings) context.Context {
	merged := clickhouse.Settings{}
	if previous, ok := ctx.Value(settingsKey{}).(clickhouse.Settings); ok {
		maps.Copy(merged, previous)
	}
	maps.Copy(merged, settings)
	return context.WithValue(ctx, settingsKey{}, merged)
}

// QueryContext is ctx for one clickhouse-go query, with the settings of
// WithSettings. It is made per query and never shared: clickhouse-go writes
// max_execution_time into the settings of the context it derives from, so
// queries running in parallel from one context that carried clickhouse-go's
// options would write one map at once (a fatal error in Go).
func QueryContext(ctx context.Context) context.Context {
	settings, ok := ctx.Value(settingsKey{}).(clickhouse.Settings)
	if !ok {
		return ctx
	}
	return clickhouse.Context(ctx, clickhouse.WithSettings(maps.Clone(settings)))
}

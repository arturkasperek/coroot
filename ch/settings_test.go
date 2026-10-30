package ch

import (
	"context"
	"testing"

	chgo "github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/assert"
)

func TestWithSettingsKeepsTheSettingsAddedBefore(t *testing.T) {
	ctx := WithSettings(context.Background(), clickhouse.Settings{"log_comment": "coroot-evaluator"})
	ctx = TimeSeriesContext(ctx)
	assert.Equal(t, clickhouse.Settings{
		"log_comment":                            "coroot-evaluator",
		"enable_time_series_table":               1,
		"enable_time_series_aggregate_functions": 1,
	}, ctx.Value(settingsKey{}))
	assert.Equal(t, context.Background(), QueryContext(context.Background()), "no settings: the context as it is")
}

func TestLowLevelSettings(t *testing.T) {
	assert.Empty(t, LowLevelSettings(context.Background()))
	ctx := TimeSeriesContext(WithSettings(context.Background(), clickhouse.Settings{"log_comment": "coroot-evaluator"}))
	assert.Equal(t, []chgo.Setting{
		{Key: "enable_time_series_aggregate_functions", Value: "1"},
		{Key: "enable_time_series_table", Value: "1"},
		{Key: "log_comment", Value: "coroot-evaluator"},
	}, LowLevelSettings(ctx))
}

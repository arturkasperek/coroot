package ch

import (
	"context"
	"testing"

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

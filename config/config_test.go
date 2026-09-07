package config

import (
	"testing"

	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

func TestNewConfig_DefaultCacheBackfillIntervalIs4h(t *testing.T) {
	cfg := NewConfig()
	assert.Equal(t, 4*timeseries.Hour, cfg.Cache.BackfillInterval)
}

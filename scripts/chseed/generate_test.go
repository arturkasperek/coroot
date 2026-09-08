package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecord_SpreadsAcrossWindowAndBothDemos(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	cfg := Config{Days: 30, Count: 100}

	first := Record(cfg, now, 0)
	last := Record(cfg, now, cfg.Count-1)

	from := now.Add(-30 * 24 * time.Hour)
	assert.Equal(t, from, first.Timestamp)
	assert.Equal(t, now, last.Timestamp)

	services := map[string]int{}
	severities := map[string]int{}
	bodies := map[string]int{}
	for i := 0; i < cfg.Count; i++ {
		r := Record(cfg, now, i)
		assert.False(t, r.Timestamp.Before(from) || r.Timestamp.After(now))
		assert.NotEmpty(t, r.ServiceName)
		assert.NotEmpty(t, r.Body)
		assert.Equal(t, "1", r.LogAttributes["chseed"])
		assert.True(t, strings.HasPrefix(r.ServiceName, "/k8s/coroot-dev/"))
		assert.Equal(t, r.ServiceName, r.ResourceAttributes["service.name"])
		assert.True(t, strings.HasPrefix(r.ResourceAttributes["container.id"], r.ServiceName+"-"))
		services[r.ServiceName]++
		severities[r.SeverityText]++
		bodies[r.Body]++
	}

	assert.Equal(t, 50, services["/k8s/coroot-dev/express-demo"])
	assert.Equal(t, 50, services["/k8s/coroot-dev/nextjs-demo"])
	assert.Greater(t, severities["INFO"], 0)
	assert.Greater(t, severities["ERROR"], 0)
	assert.Greater(t, bodies["express hello"], 0)
	assert.Greater(t, bodies["express slow path"], 0)
	assert.Greater(t, bodies["express simulated failure"], 0)
	assert.Greater(t, bodies["nextjs fetching express"], 0)
	assert.Greater(t, bodies["nextjs express fetch failed"], 0)
}

func TestRecord_AgentStyleExpressHello(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	var hello *LogRecord
	for i := 0; i < 40; i++ {
		r := Record(Config{Days: 1, Count: 40}, now, i)
		if r.Body == "express hello" {
			hello = &r
			break
		}
	}
	require.NotNil(t, hello)
	assert.Equal(t, "INFO", hello.SeverityText)
	assert.Equal(t, int32(9), hello.SeverityNumber)
	assert.Equal(t, "/api/hello", hello.LogAttributes["path"])
	assert.NotEmpty(t, hello.LogAttributes["pattern.hash"])
}

func TestRecord_IndexOutOfRangePanics(t *testing.T) {
	assert.Panics(t, func() {
		Record(Config{Days: 1, Count: 1}, time.Now(), 1)
	})
}

func TestRecord_LargeVolumeStaysInsideWindow(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	cfg := Config{Days: 7, Count: 100_000}
	from := now.Add(-7 * 24 * time.Hour)

	first := Record(cfg, now, 0)
	last := Record(cfg, now, cfg.Count-1)
	mid := Record(cfg, now, cfg.Count/2)
	overflow := Record(cfg, now, 20_000)

	assert.Equal(t, from, first.Timestamp)
	assert.Equal(t, now, last.Timestamp)
	assert.InDelta(t, from.Add(3*24*time.Hour+12*time.Hour).Unix(), mid.Timestamp.Unix(), 60)
	assert.True(t, overflow.Timestamp.After(from))
	assert.True(t, overflow.Timestamp.Before(now))
	assert.True(t, overflow.Timestamp.After(first.Timestamp))
	assert.True(t, last.Timestamp.After(overflow.Timestamp))

	million := Config{Days: 30, Count: 1_000_000}
	assert.Equal(t, now.Add(-30*24*time.Hour), Record(million, now, 0).Timestamp)
	assert.Equal(t, now, Record(million, now, million.Count-1).Timestamp)
}

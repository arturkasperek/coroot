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

func TestTraceRecord_MirrorsLogWindowAndOtelServices(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	cfg := Config{Days: 30, Count: 100}

	first := TraceRecordFor(cfg, now, 0)
	last := TraceRecordFor(cfg, now, cfg.Count-1)
	from := now.Add(-30 * 24 * time.Hour)
	assert.Equal(t, from, first.Timestamp)
	assert.Equal(t, now, last.Timestamp)
	assert.Equal(t, Record(cfg, now, 0).Timestamp, first.Timestamp)
	assert.Equal(t, Record(cfg, now, cfg.Count-1).Timestamp, last.Timestamp)

	services := map[string]int{}
	routes := map[string]int{}
	for i := 0; i < cfg.Count; i++ {
		r := TraceRecordFor(cfg, now, i)
		assert.False(t, r.Timestamp.Before(from) || r.Timestamp.After(now))
		assert.Equal(t, "", r.ParentSpanId)
		assert.Equal(t, "SPAN_KIND_SERVER", r.SpanKind)
		assert.Equal(t, "coroot-dev", r.ResourceAttributes["k8s.namespace.name"])
		assert.Equal(t, r.ServiceName, r.ResourceAttributes["service.name"])
		assert.Equal(t, "1", r.SpanAttributes["chseed"])
		assert.Equal(t, seedHistPeer, r.SpanAttributes["net.peer.name"])
		assert.Equal(t, "GET", r.SpanAttributes["http.method"])
		assert.True(t, strings.HasPrefix(r.TraceId, seedTraceIDPrefix))
		assert.Len(t, r.TraceId, 32)
		assert.Len(t, r.SpanId, 16)
		assert.Equal(t, r.SpanAttributes["http.route"], r.SpanAttributes["http.target"])
		assert.Equal(t, "GET "+r.SpanAttributes["http.route"], r.SpanName)
		services[r.ServiceName]++
		routes[r.SpanAttributes["http.route"]]++
	}
	assert.Equal(t, 50, services["express-demo"])
	assert.Equal(t, 50, services["nextjs-demo"])
	assert.Greater(t, routes["/api/hello"], 0)
	assert.Greater(t, routes["/api/slow"], 0)
	assert.Greater(t, routes["/api/error"], 0)
	assert.Greater(t, routes["/"], 0)
	assert.Greater(t, routes["/chain"], 0)
}

func TestTraceRecord_ErrorAndSlow(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	cfg := Config{Days: 1, Count: 40}

	var errSpan, slow *TraceRecord
	for i := 0; i < cfg.Count; i++ {
		r := TraceRecordFor(cfg, now, i)
		if r.SpanAttributes["http.route"] == "/api/error" && errSpan == nil {
			cp := r
			errSpan = &cp
		}
		if r.SpanAttributes["http.route"] == "/api/slow" && slow == nil {
			cp := r
			slow = &cp
		}
	}
	require.NotNil(t, errSpan)
	require.NotNil(t, slow)
	assert.Equal(t, "STATUS_CODE_ERROR", errSpan.StatusCode)
	assert.Equal(t, "express-demo", errSpan.ServiceName)
	assert.Equal(t, "STATUS_CODE_UNSET", slow.StatusCode)
	assert.Greater(t, slow.Duration, errSpan.Duration)
}

func TestTraceRecord_IndexOutOfRangePanics(t *testing.T) {
	assert.Panics(t, func() {
		TraceRecordFor(Config{Days: 1, Count: 1}, time.Now(), 1)
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

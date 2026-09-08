package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseArgs_DaysAndThousandsOfLogs(t *testing.T) {
	cfg, err := ParseArgs([]string{"30", "1000"})
	require.NoError(t, err)
	assert.Equal(t, 30, cfg.Days)
	assert.Equal(t, 1_000_000, cfg.Count)
}

func TestParseArgs_SmallerVolume(t *testing.T) {
	cfg, err := ParseArgs([]string{"7", "1"})
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.Days)
	assert.Equal(t, 1000, cfg.Count)
}

func TestParseArgs_RejectsBadInput(t *testing.T) {
	cases := [][]string{
		nil,
		{},
		{"30"},
		{"30", "1000", "extra"},
		{"0", "1000"},
		{"-1", "1000"},
		{"30", "0"},
		{"abc", "1000"},
		{"30", "x"},
	}
	for _, args := range cases {
		_, err := ParseArgs(args)
		assert.Error(t, err, "args=%v", args)
	}
}

func TestProjectDatabase(t *testing.T) {
	assert.Equal(t, "coroot_49jjxrcg", ProjectDatabase("49jjxrcg"))
}

func TestClickHouseHTTPAddr(t *testing.T) {
	addr, err := ClickHouseHTTPAddr("http://127.0.0.1:18123")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:18123", addr)

	addr, err = ClickHouseHTTPAddr("http://127.0.0.1:18123/")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:18123", addr)

	_, err = ClickHouseHTTPAddr("not a url")
	assert.Error(t, err)
}

func TestTTLSeconds_ExtendsPastSeedWindow(t *testing.T) {
	assert.Equal(t, uint64(7*24*time.Hour/time.Second), TTLSeconds(3))
	assert.Equal(t, uint64(8*24*time.Hour/time.Second), TTLSeconds(7))
	assert.Equal(t, uint64(31*24*time.Hour/time.Second), TTLSeconds(30))
}

func TestPickLogsDatabase(t *testing.T) {
	name, err := PickLogsDatabase([]string{"default"})
	require.NoError(t, err)
	assert.Equal(t, "default", name)

	name, err = PickLogsDatabase([]string{"default", "coroot_zz", "coroot_aa"})
	require.NoError(t, err)
	assert.Equal(t, "coroot_aa", name)

	_, err = PickLogsDatabase(nil)
	assert.Error(t, err)
}

func TestModifyTTLQuery(t *testing.T) {
	assert.Equal(t,
		"ALTER TABLE otel_logs MODIFY TTL toDateTime(Timestamp) + toIntervalSecond(2678400)",
		ModifyTTLQuery("otel_logs", "Timestamp", 2678400),
	)
}

func TestParseUserProjects(t *testing.T) {
	ids, err := ParseUserProjects([]byte(`{"projects":[{"id":"49jjxrcg","name":"default"}]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"49jjxrcg"}, ids)
}

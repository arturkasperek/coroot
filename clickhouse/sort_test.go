package clickhouse

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTraceListOrderBy(t *testing.T) {
	cases := []struct {
		name string
		in   Sort
		want string
	}{
		{name: "empty", in: Sort{}, want: ""},
		{name: "date desc is default", in: Sort{By: "date", Dir: "desc"}, want: ""},
		{name: "date asc", in: Sort{By: "date", Dir: "asc"}, want: "Timestamp ASC"},
		{name: "date missing dir defaults desc", in: Sort{By: "date"}, want: ""},
		{name: "date invalid dir defaults desc", in: Sort{By: "date", Dir: "sideways"}, want: ""},
		{name: "DATE DESC mixed case", in: Sort{By: "DATE", Dir: "DESC"}, want: ""},
		{name: "Date ASC mixed case", in: Sort{By: "Date", Dir: "ASC"}, want: "Timestamp ASC"},
		{name: "duration desc", in: Sort{By: "duration", Dir: "desc"}, want: "Duration DESC"},
		{name: "duration asc", in: Sort{By: "duration", Dir: "asc"}, want: "Duration ASC"},
		{name: "duration missing dir defaults desc", in: Sort{By: "duration"}, want: "Duration DESC"},
		{name: "DURATION ASC mixed case", in: Sort{By: "DURATION", Dir: "ASC"}, want: "Duration ASC"},
		{name: "unknown by", in: Sort{By: "message", Dir: "desc"}, want: ""},
		{name: "sql injection by", in: Sort{By: "drop table", Dir: "desc"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, TraceListOrderBy(tc.in))
		})
	}
}

func TestLogListOrderBy(t *testing.T) {
	cases := []struct {
		name string
		in   Sort
		want string
	}{
		{name: "empty", in: Sort{}, want: ""},
		{name: "date desc is default", in: Sort{By: "date", Dir: "desc"}, want: ""},
		{name: "date asc", in: Sort{By: "date", Dir: "asc"}, want: "Timestamp ASC"},
		{name: "date missing dir defaults desc", in: Sort{By: "date"}, want: ""},
		{name: "DATE ASC mixed case", in: Sort{By: "DATE", Dir: "ASC"}, want: "Timestamp ASC"},
		{name: "duration is ignored", in: Sort{By: "duration", Dir: "desc"}, want: ""},
		{name: "duration asc is ignored", in: Sort{By: "duration", Dir: "asc"}, want: ""},
		{name: "unknown by", in: Sort{By: "severity", Dir: "desc"}, want: ""},
		{name: "sql injection by", in: Sort{By: "drop table", Dir: "asc"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, LogListOrderBy(tc.in))
		})
	}
}

func TestLogsListSQLDateDescUsesTimestampCutoff(t *testing.T) {
	q := logsListSQL("", "SeverityText = 'INFO'", "100")
	assert.Contains(t, q, "ORDER BY Timestamp DESC")
	assert.Contains(t, q, "Timestamp >= (")
	assert.Contains(t, q, "LIMIT 100")
}

func TestLogsListSQLDateAscUsesTimestampCutoff(t *testing.T) {
	q := logsListSQL("Timestamp ASC", "SeverityText = 'INFO'", "100")
	assert.Contains(t, q, "ORDER BY Timestamp ASC")
	assert.Contains(t, q, "Timestamp <= (")
	assert.Contains(t, q, "SELECT Timestamp FROM @@table_otel_logs@@ WHERE SeverityText = 'INFO' ORDER BY Timestamp ASC LIMIT 100")
	assert.NotContains(t, q, "Timestamp >= (")
}

func TestLogsListSQLDateAscCutoffSelectsOnlyTimestamp(t *testing.T) {
	q := logsListSQL("Timestamp ASC", "Timestamp BETWEEN @from AND @to", "100")
	assert.Equal(t, 1, strings.Count(q, "Body"))
	assert.Equal(t, 1, strings.Count(q, "ResourceAttributes"))
	assert.Equal(t, 1, strings.Count(q, "LogAttributes"))
	assert.Contains(t, q, "SELECT Timestamp FROM @@table_otel_logs@@ WHERE Timestamp BETWEEN @from AND @to ORDER BY Timestamp ASC LIMIT 100")
}

func TestSpanListUsesCutoffForDateDescAndAsc(t *testing.T) {
	extra, order := timestampLimitCutoff("", "@@table_otel_traces@@", "ServiceName = 'api'", "100")
	assert.Contains(t, extra, "Timestamp >= (")
	assert.Equal(t, "Timestamp DESC", order)

	extra, order = timestampLimitCutoff("Timestamp ASC", "@@table_otel_traces@@", "ServiceName = 'api'", "100")
	assert.Contains(t, extra, "Timestamp <= (")
	assert.Equal(t, "Timestamp ASC", order)

	extra, order = timestampLimitCutoff("Duration DESC", "@@table_otel_traces@@", "ServiceName = 'api'", "100")
	assert.Equal(t, "", extra)
	assert.Equal(t, "Duration DESC", order)
}

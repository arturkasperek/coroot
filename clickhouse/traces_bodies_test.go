package clickhouse

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSpanColumnsBodiesOnlyOnRequest(t *testing.T) {
	assert.NotContains(t, spanColumns(false), "RequestBody")
	assert.NotContains(t, spanColumns(false), "ResponseBody")
	assert.Contains(t, spanColumns(true), "RequestBody")
	assert.Contains(t, spanColumns(true), "ResponseBody")
	// one list for every span query, so no query can quietly lose a column
	assert.True(t, strings.HasPrefix(spanColumns(false), "Timestamp, TraceId, SpanId, ParentSpanId"))
	assert.True(t, strings.HasPrefix(spanColumns(true), spanColumns(false)))
}

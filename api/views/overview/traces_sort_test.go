package overview

import (
	"testing"

	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

func TestParseTraceQuerySort(t *testing.T) {
	q := parseQuery(`{"view":"traces","sort":{"by":"duration","dir":"desc"}}`, timeseries.Context{})
	assert.Equal(t, clickhouse.Sort{By: "duration", Dir: "desc"}, q.Sort)
	assert.Equal(t, "Duration DESC", clickhouse.TraceListOrderBy(q.Sort))
}

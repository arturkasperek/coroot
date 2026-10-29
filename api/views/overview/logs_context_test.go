package overview

import (
	"testing"

	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/stretchr/testify/assert"
)

func TestLogsContextEndsAtTheRequestedEnd(t *testing.T) {
	// the world of the view ends at the last evaluated step, a minute before now
	w := model.NewWorld(907, 1860, 15, 15)

	ctx := logsContext(w, TimeRange{From: 907, To: 1937, Live: true})
	assert.Equal(t, timeseries.Context{From: 907, To: 1937, Step: 15}, ctx)

	assert.Equal(t, w.Ctx, logsContext(w, TimeRange{}), "no requested range: the world's")
}

package promql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectors(t *testing.T) {
	for q, want := range map[string][]string{
		`up`: {"up"},
		`sum by(app_id) (rate(container_http_requests_total{app_id!=""}[$RANGE])) or rate(container_http_requests_total{app_id=""}[$RANGE])`: {"container_http_requests_total"},
		`kube_pod_status_phase > 0`:                                                      {"kube_pod_status_phase"},
		`container_oom_kills_total % 10000000`:                                           {"container_oom_kills_total"},
		`time() - mongo_rs_last_applied_timestamp_ms/1000`:                               {"mongo_rs_last_applied_timestamp_ms"},
		`rate(a[$RANGE]) / ignoring(mode) group_left sum(rate(b[$RANGE])) without(mode)`: {"a", "b"},
		`{__name__="exact"}`:                                                             {"exact"},
		`{job="x"}`:                                                                      nil, // no metric name: can't tell
		`{__name__=~"up|node_info"}`:                                                     nil,
	} {
		got, err := Selectors(q)
		require.NoError(t, err, q)
		assert.ElementsMatch(t, want, got, q)
	}
	_, err := Selectors(`rate(`)
	assert.Error(t, err)
}

func TestSelectorsOfRecordingRuleNames(t *testing.T) {
	// the constructor's recording rules are names, not PromQL
	got, err := Selectors(`rr_application_log_messages`)
	require.NoError(t, err)
	assert.Equal(t, []string{"rr_application_log_messages"}, got)
}

func TestAddExtraSelector(t *testing.T) {
	q, err := AddExtraSelector(`sum by (a) (rate(x{b="1"}[5m])) / y`, `{job="api",ns="a"}`)
	require.NoError(t, err)
	assert.Contains(t, q, `x{b="1",job="api",ns="a"}`)
	assert.Contains(t, q, `y{job="api",ns="a"}`)
	same, err := AddExtraSelector("up", "")
	require.NoError(t, err)
	assert.Equal(t, "up", same)
	_, err = AddExtraSelector("up", "{")
	assert.Error(t, err)
}

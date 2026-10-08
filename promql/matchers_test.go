package promql

import (
	"testing"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatcherSQL(t *testing.T) {
	for _, tc := range []struct {
		m    *labels.Matcher
		sql  string
		args []any
	}{
		{labels.MustNewMatcher(labels.MatchEqual, "__name__", "up"), "metric_name = @m0", []any{"up"}},
		{labels.MustNewMatcher(labels.MatchNotEqual, "__name__", "up"), "metric_name != @m0", []any{"up"}},
		{labels.MustNewMatcher(labels.MatchEqual, "job", "api"), "tags[@m0n] = @m0", []any{"job", "api"}},
		{labels.MustNewMatcher(labels.MatchNotEqual, "job", "api"), "tags[@m0n] != @m0", []any{"job", "api"}},
		// PromQL regexes are anchored; ClickHouse match() is not
		{labels.MustNewMatcher(labels.MatchRegexp, "job", "a.*"), "match(tags[@m0n], @m0)", []any{"job", "^(?:a.*)$"}},
		{labels.MustNewMatcher(labels.MatchNotRegexp, "job", "a|b"), "NOT match(tags[@m0n], @m0)", []any{"job", "^(?:a|b)$"}},
		{labels.MustNewMatcher(labels.MatchRegexp, "__name__", "up|down"), "match(metric_name, @m0)", []any{"^(?:up|down)$"}},
	} {
		sql, args := matcherSQL(0, tc.m)
		assert.Equal(t, tc.sql, sql)
		require.Len(t, args, len(tc.args))
		for i, a := range tc.args {
			assert.Equal(t, a, args[i].(chdriver.NamedValue).Value, "%s arg %d", tc.sql, i)
		}
	}
}

func TestMatcherSetsSQLNumbersTheParameters(t *testing.T) {
	where, args := matcherSetsSQL([][]*labels.Matcher{{
		labels.MustNewMatcher(labels.MatchEqual, "__name__", "up"),
		labels.MustNewMatcher(labels.MatchEqual, "job", "api"),
	}})
	assert.Equal(t, "(metric_name = @m0 AND tags[@m1n] = @m1)", where)
	assert.Len(t, args, 3)

	where, args = matcherSetsSQL([][]*labels.Matcher{
		{labels.MustNewMatcher(labels.MatchEqual, "job", "api")},
		{labels.MustNewMatcher(labels.MatchEqual, "job", "db")},
	})
	assert.Equal(t, "(tags[@m0n] = @m0) OR (tags[@m1n] = @m1)", where, "the sets are ORed, numbered together")
	assert.Len(t, args, 4)

	empty, noArgs := matcherSetsSQL(nil)
	assert.Equal(t, "1", empty)
	assert.Empty(t, noArgs)
}

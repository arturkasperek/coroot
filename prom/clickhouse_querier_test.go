package prom

import (
	"strings"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/assert"
)

func matcher(t labels.MatchType, name, value string) *labels.Matcher {
	return labels.MustNewMatcher(t, name, value)
}

func newQuerier() *clickhouseQuerier {
	return &clickhouseQuerier{mint: 1_700_000_000_000, maxt: 1_700_003_600_000}
}

func TestSelectSQLNoLabelMatchers(t *testing.T) {
	sql := newQuerier().selectSQL([]*labels.Matcher{matcher(labels.MatchEqual, labels.MetricName, "up")})
	assert.Contains(t, sql, "FROM @@table_metrics_samples@@")
	assert.Contains(t, sql, "FROM @@table_metrics_series@@")
	assert.Contains(t, sql, "MetricName = 'up'")
	assert.Contains(t, sql, "Timestamp >= toDateTime(1700000000) AND Timestamp <= toDateTime(1700003600)")
	// samples carry no labels, and without a label matcher there is nothing to resolve
	assert.NotContains(t, sql, "Labels[")
	assert.NotContains(t, sql, "MetricHash IN")
	assert.Contains(t, sql, "GROUP BY MetricHash")
}

func TestSelectSQLLabelMatchersAreResolvedOnTheSeriesTable(t *testing.T) {
	sql := newQuerier().selectSQL([]*labels.Matcher{
		matcher(labels.MatchEqual, labels.MetricName, "up"),
		matcher(labels.MatchEqual, "namespace", "a"),
		matcher(labels.MatchRegexp, "pod", "x.*"),
	})
	assert.Contains(t, sql, "MetricHash IN (SELECT MetricHash FROM @@table_metrics_series@@ WHERE MetricName = 'up' AND Labels['namespace'] = 'a' AND match(Labels['pod']")
	// the samples subquery filters by name and time only, never by a label
	samples := sql[strings.Index(sql, "FROM @@table_metrics_samples@@"):strings.Index(sql, ") AS d")]
	assert.Equal(t, 2, strings.Count(samples, "Labels['"), "only inside MetricHash IN (...)")
	// and the series that are joined back are filtered the same way
	series := sql[strings.Index(sql, "INNER JOIN"):]
	assert.Contains(t, series, "Labels['namespace'] = 'a'")
}

func TestSelectSQLWithoutAnyMatcher(t *testing.T) {
	sql := newQuerier().selectSQL(nil)
	assert.Contains(t, sql, "WHERE 1")
}

func TestSelectSQLEscapesValues(t *testing.T) {
	sql := newQuerier().selectSQL([]*labels.Matcher{matcher(labels.MatchEqual, "job", `a'b\c`)})
	assert.NotContains(t, sql, `'a'b`)
}

func TestLabelValuesSQLReadsTheSeriesTable(t *testing.T) {
	q := newQuerier()
	sql := q.labelValuesSQL("namespace", []*labels.Matcher{matcher(labels.MatchEqual, labels.MetricName, "up")})
	assert.Contains(t, sql, "FROM @@table_metrics_series@@")
	assert.Contains(t, sql, "Labels['namespace']")
	assert.Contains(t, sql, "LastSeen >= toDateTime(1700000000)")
	assert.Contains(t, sql, "MetricName = 'up'")
	assert.NotContains(t, sql, "metrics_samples")

	names := q.labelValuesSQL(labels.MetricName, nil)
	assert.Contains(t, names, "SELECT DISTINCT MetricName as LabelValue")
}

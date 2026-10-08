package promql

import (
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/prometheus/prometheus/model/labels"
)

// matcherSQL is a label matcher over the tags table of a TimeSeries table
// (metric_name, tags). Values are parameters, never part of the text. PromQL
// regexes are fully anchored, ClickHouse match() is not, and an empty label is
// an absent label (tags['x'] of a missing key is ”).
// The parameters are named after index, so matchers of one query do not clash.
func matcherSQL(index int, matcher *labels.Matcher) (string, []any) {
	valueName := fmt.Sprintf("m%d", index)
	value := matcher.Value
	if matcher.Type == labels.MatchRegexp || matcher.Type == labels.MatchNotRegexp {
		value = "^(?:" + value + ")$"
	}
	args := []any{}
	column := "metric_name"
	if matcher.Name != labels.MetricName {
		column = fmt.Sprintf("tags[@%sn]", valueName)
		args = append(args, clickhouse.Named(valueName+"n", matcher.Name))
	}
	args = append(args, clickhouse.Named(valueName, value))
	switch matcher.Type {
	case labels.MatchEqual:
		return fmt.Sprintf("%s = @%s", column, valueName), args
	case labels.MatchNotEqual:
		return fmt.Sprintf("%s != @%s", column, valueName), args
	case labels.MatchRegexp:
		return fmt.Sprintf("match(%s, @%s)", column, valueName), args
	default:
		return fmt.Sprintf("NOT match(%s, @%s)", column, valueName), args
	}
}

// matcherSetsSQL ORs sets of matchers (the match[] parameter of the Prometheus
// API), ANDing the matchers inside a set; the parameters of all sets are
// numbered together. "1" when there are no sets.
func matcherSetsSQL(sets [][]*labels.Matcher) (string, []any) {
	if len(sets) == 0 {
		return "1", nil
	}
	var setConditions []string
	var args []any
	index := 0
	for _, set := range sets {
		var conditions []string
		for _, matcher := range set {
			condition, matcherArgs := matcherSQL(index, matcher)
			index++
			conditions = append(conditions, condition)
			args = append(args, matcherArgs...)
		}
		if len(conditions) == 0 {
			conditions = []string{"1"}
		}
		setConditions = append(setConditions, "("+strings.Join(conditions, " AND ")+")")
	}
	return strings.Join(setConditions, " OR "), args
}

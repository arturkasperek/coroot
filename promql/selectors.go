// Package promql evaluates PromQL in ClickHouse (a TimeSeries table, the
// prometheusQuery / prometheusQueryRange table functions) and gives the results
// in the shapes the rest of Coroot uses.
package promql

import (
	"slices"
	"strings"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// Selectors returns the metric names a query reads, or nil when it cannot tell
// (a selector without a metric name, or with a regex on the name): such a query
// has to be evaluated. $RANGE is accepted.
func Selectors(query string) ([]string, error) {
	expr, err := parser.ParseExpr(strings.ReplaceAll(query, "$RANGE", "1m"))
	if err != nil {
		return nil, err
	}
	var names []string
	unknown := false
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		vs, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		name := vs.Name
		if name == "" {
			for _, m := range vs.LabelMatchers {
				if m.Name == labels.MetricName && m.Type == labels.MatchEqual {
					name = m.Value
				}
			}
		}
		if name == "" {
			unknown = true
			return nil
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
		return nil
	})
	if unknown {
		return nil, nil
	}
	return names, nil
}

// AddExtraSelector adds the matchers of extraSelector (e.g. `{job="a"}`) to every
// selector of the query.
func AddExtraSelector(query string, extraSelector string) (string, error) {
	if extraSelector == "" {
		return query, nil
	}
	extra, err := parser.ParseMetricSelector(extraSelector)
	if err != nil {
		return "", err
	}
	expr, err := parser.ParseExpr(query)
	if err != nil {
		return "", err
	}
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		if vs, ok := node.(*parser.VectorSelector); ok {
			vs.LabelMatchers = append(vs.LabelMatchers, extra...)
		}
		return nil
	})
	return expr.String(), nil
}

package overview

import (
	"sort"

	"github.com/coroot/coroot/clickhouse"
)

func traceFacetGroupsFromMerged(merged map[string]map[string]uint64) []clickhouse.FacetGroup {
	order := []string{"Namespace", "ServiceName", "SpanName"}
	groups := []clickhouse.FacetGroup{}
	for _, key := range order {
		counts := merged[key]
		if counts == nil {
			continue
		}
		g := clickhouse.FacetGroup{Key: key}
		for v, n := range counts {
			g.Values = append(g.Values, clickhouse.FacetValue{Value: v, Count: n})
		}
		if key == "Namespace" {
			g.Values = completeNamespaceFacets(g.Values)
		} else {
			sort.Slice(g.Values, func(i, j int) bool {
				if g.Values[i].Count != g.Values[j].Count {
					return g.Values[i].Count > g.Values[j].Count
				}
				return g.Values[i].Value < g.Values[j].Value
			})
		}
		groups = append(groups, g)
	}
	return groups
}

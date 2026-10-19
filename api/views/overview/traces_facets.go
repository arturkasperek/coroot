package overview

import (
	"slices"
	"sort"

	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/clickhouse"
)

func traceFacetGroupsFromMerged(merged map[string]map[string]uint64) []clickhouse.FacetGroup {
	order := []string{"Source", "Status", "Namespace", "Server", "Client", "ApiRoute", "SpanName"}
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
		switch key {
		case "Namespace":
			g.Values = completeNamespaceFacets(g.Values)
		case "Status": // in the order of the requests chart
			sort.Slice(g.Values, func(i, j int) bool {
				return slices.Index(ch.TraceStatusClasses, g.Values[i].Value) < slices.Index(ch.TraceStatusClasses, g.Values[j].Value)
			})
		default:
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

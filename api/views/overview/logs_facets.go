package overview

import (
	"sort"

	"github.com/coroot/coroot/clickhouse"
	"github.com/coroot/coroot/model"
)

var severityFacetOrder = []string{"unknown", "info", "warning", "error"}
var sourceFacetOrder = []string{string(model.LogSourceAgent), string(model.LogSourceOtel)}

func mergeFacetValues(dst map[string]uint64, values []clickhouse.FacetValue) {
	if dst == nil {
		return
	}
	for _, v := range values {
		if v.Value == "" {
			continue
		}
		dst[v.Value] += v.Count
	}
}

func completeSeverityFacets(values []clickhouse.FacetValue) []clickhouse.FacetValue {
	by := map[string]uint64{}
	for _, v := range values {
		by[v.Value] = v.Count
	}
	out := make([]clickhouse.FacetValue, 0, len(severityFacetOrder))
	for _, label := range severityFacetOrder {
		out = append(out, clickhouse.FacetValue{Value: label, Count: by[label]})
	}
	return out
}

func completeSourceFacets(values []clickhouse.FacetValue) []clickhouse.FacetValue {
	by := map[string]uint64{}
	for _, v := range values {
		by[v.Value] = v.Count
	}
	out := make([]clickhouse.FacetValue, 0, len(sourceFacetOrder))
	for _, label := range sourceFacetOrder {
		out = append(out, clickhouse.FacetValue{Value: label, Count: by[label]})
	}
	return out
}

func facetGroupsFromMerged(merged map[string]map[string]uint64) []clickhouse.FacetGroup {
	order := []string{"Source", "Severity", "Cluster", "service.name", "host.name"}
	groups := []clickhouse.FacetGroup{}
	for _, key := range order {
		counts := merged[key]
		if counts == nil {
			continue
		}
		g := clickhouse.FacetGroup{Key: key}
		switch key {
		case "Severity":
			vals := make([]clickhouse.FacetValue, 0, len(counts))
			for v, n := range counts {
				vals = append(vals, clickhouse.FacetValue{Value: v, Count: n})
			}
			g.Values = completeSeverityFacets(vals)
		case "Source":
			vals := make([]clickhouse.FacetValue, 0, len(counts))
			for v, n := range counts {
				vals = append(vals, clickhouse.FacetValue{Value: v, Count: n})
			}
			g.Values = completeSourceFacets(vals)
		default:
			for v, n := range counts {
				g.Values = append(g.Values, clickhouse.FacetValue{Value: v, Count: n})
			}
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

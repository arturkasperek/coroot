package clickhouse

import (
	"context"
	"fmt"
	"sort"
	"strings"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/model"
)

const logNamespaceNA = "n/a"

type FacetValue struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}

type FacetGroup struct {
	Key    string       `json:"key"`
	Values []FacetValue `json:"values"`
}

func facetCountSQL(name string) (string, *string, bool) {
	attr := name
	switch name {
	case "Severity":
		return "SELECT multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1), count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY 1", &attr, true
	case "service.name":
		return "SELECT ServiceName, count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY ServiceName HAVING ServiceName != '' ORDER BY count(1) DESC, ServiceName LIMIT 1000", &attr, true
	case "host.name":
		return "SELECT if(LogAttributes[@attr] != '', LogAttributes[@attr], ResourceAttributes[@attr]) AS v, count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY v HAVING v != '' ORDER BY count(1) DESC, v LIMIT 1000", &attr, true
	case "Cluster":
		return "SELECT count(1) FROM @@table_otel_logs@@ WHERE %s", &attr, true
	case "Source":
		return "SELECT if(startsWith(ServiceName, '/'), 'agent', 'otel'), count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY 1", &attr, true
	case "Namespace":
		return "SELECT Namespace, count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY 1", &attr, true
	case "Application":
		return "SELECT Application AS v, count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY v HAVING v != '' ORDER BY count(1) DESC, v LIMIT 1000", &attr, true
	default:
		return "", nil, false
	}
}

// rollupFacetCountSQL is facetCountSQL over otel_logs_rollup. The host.name
// facet is the one attribute the rollup keeps.
func rollupFacetCountSQL(name string) (string, bool) {
	const from = " FROM @@table_otel_logs_rollup@@ WHERE %s"
	switch name {
	case "Severity":
		return "SELECT multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1), sum(Count)" + from + " GROUP BY 1", true
	case "service.name":
		return "SELECT ServiceName, sum(Count)" + from + " GROUP BY ServiceName HAVING ServiceName != '' ORDER BY sum(Count) DESC, ServiceName LIMIT 1000", true
	case "host.name":
		return "SELECT if(HostLog != '', HostLog, HostRes) AS v, sum(Count)" + from + " GROUP BY v HAVING v != '' ORDER BY sum(Count) DESC, v LIMIT 1000", true
	case "Cluster":
		return "SELECT sum(Count)" + from, true
	case "Source":
		return "SELECT if(startsWith(ServiceName, '/'), 'agent', 'otel'), sum(Count)" + from + " GROUP BY 1", true
	case "Namespace":
		return "SELECT Namespace, sum(Count)" + from + " GROUP BY 1", true
	case "Application":
		return "SELECT Application AS v, sum(Count)" + from + " GROUP BY v HAVING v != '' ORDER BY sum(Count) DESC, v LIMIT 1000", true
	}
	return "", false
}

// GetLogFacetCounts is answered by the per-minute rollup when the query allows it.
func (c *Client) GetLogFacetCounts(ctx context.Context, query LogQuery, name string) ([]FacetValue, error) {
	_, attr, ok := facetCountSQL(name)
	if !ok {
		return nil, fmt.Errorf("unsupported log facet %q", name)
	}
	if where, args, ok := query.rollupFilters(attr); ok {
		rsql, _ := rollupFacetCountSQL(name)
		return c.queryLogFacetCounts(ctx, name, fmt.Sprintf(rsql, strings.Join(where, " AND ")), args)
	}
	q, args := rawLogFacetCountSQL(query, name)
	return c.queryLogFacetCounts(ctx, name, q, args)
}

func rawLogFacetCountSQL(query LogQuery, name string) (string, []any) {
	sqlFmt, attr, _ := facetCountSQL(name)
	where, args := query.filters(attr)
	if name == "host.name" {
		args = append(args, chgo.Named("attr", name))
	}
	return fmt.Sprintf(sqlFmt, strings.Join(where, " AND ")), args
}

func (c *Client) queryLogFacetCounts(ctx context.Context, name, q string, args []any) ([]FacetValue, error) {
	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	switch name {
	case "Cluster":
		var n uint64
		if rows.Next() {
			if err = rows.Scan(&n); err != nil {
				return nil, err
			}
		}
		return []FacetValue{{Value: c.project.Name, Count: n}}, nil
	case "Severity":
		by := map[string]uint64{}
		var sev int64
		var n uint64
		for rows.Next() {
			if err = rows.Scan(&sev, &n); err != nil {
				return nil, err
			}
			by[model.Severity(sev).String()] = n
		}
		out := make([]FacetValue, 0, 4)
		for _, label := range []string{"unknown", "info", "warning", "error"} {
			out = append(out, FacetValue{Value: label, Count: by[label]})
		}
		return out, nil
	case "Source":
		by := map[string]uint64{}
		var v string
		var n uint64
		for rows.Next() {
			if err = rows.Scan(&v, &n); err != nil {
				return nil, err
			}
			by[v] = n
		}
		out := make([]FacetValue, 0, 2)
		for _, label := range []string{string(model.LogSourceAgent), string(model.LogSourceOtel)} {
			out = append(out, FacetValue{Value: label, Count: by[label]})
		}
		return out, nil
	case "Namespace":
		by := map[string]uint64{}
		var v string
		var n uint64
		for rows.Next() {
			if err = rows.Scan(&v, &n); err != nil {
				return nil, err
			}
			by[v] = n
		}
		if _, ok := by[logNamespaceNA]; !ok {
			by[logNamespaceNA] = 0
		}
		var names []string
		for name := range by {
			if name != logNamespaceNA {
				names = append(names, name)
			}
		}
		sort.Slice(names, func(i, j int) bool {
			if by[names[i]] != by[names[j]] {
				return by[names[i]] > by[names[j]]
			}
			return names[i] < names[j]
		})
		out := make([]FacetValue, 0, len(by))
		for _, name := range names {
			out = append(out, FacetValue{Value: name, Count: by[name]})
		}
		out = append(out, FacetValue{Value: logNamespaceNA, Count: by[logNamespaceNA]})
		return out, nil
	default:
		var out []FacetValue
		var v string
		var n uint64
		for rows.Next() {
			if err = rows.Scan(&v, &n); err != nil {
				return nil, err
			}
			if v == "" {
				continue
			}
			out = append(out, FacetValue{Value: v, Count: n})
		}
		return out, nil
	}
}

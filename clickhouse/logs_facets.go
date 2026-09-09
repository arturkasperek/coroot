package clickhouse

import (
	"context"
	"fmt"
	"sort"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/model"
)

const maxLogFacetValues = 1000

const logNamespaceNA = "n/a"

func logNamespaceExpr() string {
	return `if(startsWith(ServiceName, '/k8s'), if(arrayElement(splitByChar('/', ServiceName), 3) = '', 'n/a', arrayElement(splitByChar('/', ServiceName), 3)), if(ResourceAttributes['k8s.namespace.name'] != '', ResourceAttributes['k8s.namespace.name'], if(LogAttributes['k8s.namespace.name'] != '', LogAttributes['k8s.namespace.name'], 'n/a')))`
}

func logApplicationExpr() string {
	return `if(startsWith(ServiceName, '/k8s'), nullIf(arrayElement(splitByChar('/', ServiceName), length(splitByChar('/', ServiceName))), ''), ServiceName)`
}

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
		return "SELECT " + logNamespaceExpr() + ", count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY 1", &attr, true
	case "Application":
		return "SELECT " + logApplicationExpr() + " AS v, count(1) FROM @@table_otel_logs@@ WHERE %s GROUP BY v HAVING v != '' ORDER BY count(1) DESC, v LIMIT 1000", &attr, true
	default:
		return "", nil, false
	}
}

func (c *Client) GetLogFacetCounts(ctx context.Context, query LogQuery, name string) ([]FacetValue, error) {
	sqlFmt, attr, ok := facetCountSQL(name)
	if !ok {
		return nil, fmt.Errorf("unsupported log facet %q", name)
	}
	where, args := query.filters(attr)
	q := fmt.Sprintf(sqlFmt, strings.Join(where, " AND "))
	if name == "host.name" {
		args = append(args, ch.Named("attr", name))
	}
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

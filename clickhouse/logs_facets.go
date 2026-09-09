package clickhouse

import (
	"context"
	"fmt"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/model"
)

const maxLogFacetValues = 1000

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

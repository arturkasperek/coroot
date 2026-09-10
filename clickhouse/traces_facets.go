package clickhouse

import (
	"context"
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const maxTraceFacetValues = 1000

func traceNamespaceExpr() string {
	return `if(ResourceAttributes['k8s.namespace.name'] != '', ResourceAttributes['k8s.namespace.name'], 'n/a')`
}

func traceFacetSelectSQL(field string, fromMV bool) (string, bool) {
	switch field {
	case "ServiceName", "SpanName":
	case "Namespace":
		if fromMV {
			return "", false
		}
		return fmt.Sprintf(
			"SELECT %s, count(1) FROM @@table_otel_traces@@ WHERE %%s GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT %d",
			traceNamespaceExpr(), maxTraceFacetValues,
		), true
	default:
		return "", false
	}
	if fromMV {
		return fmt.Sprintf(
			"SELECT %s, sum(Total) FROM @@table_otel_traces_histogram@@ WHERE %%s GROUP BY 1 HAVING %s != '' ORDER BY 2 DESC, 1 LIMIT %d",
			field, field, maxTraceFacetValues,
		), true
	}
	return fmt.Sprintf(
		"SELECT %s, count(1) FROM @@table_otel_traces@@ WHERE %%s GROUP BY 1 HAVING %s != '' ORDER BY 2 DESC, 1 LIMIT %d",
		field, field, maxTraceFacetValues,
	), true
}

func buildTraceFacetQuery(q SpanQuery, field string, fromMV bool) (string, []any, bool) {
	sqlFmt, ok := traceFacetSelectSQL(field, fromMV)
	if !ok {
		return "", nil, false
	}
	filter, args := q.rootSpansFilter(fromMV, field)
	if fromMV {
		filter = append(filter, "Timestamp >= @tsFrom AND Timestamp < @tsTo")
	} else {
		filter = append(filter, "Timestamp BETWEEN @tsFrom AND @tsTo")
		durFilter, durArgs := q.DurationFilter()
		if durFilter != "" {
			filter = append(filter, durFilter)
			args = append(args, durArgs...)
		}
	}
	args = append(args,
		clickhouse.DateNamed("tsFrom", q.TsFrom.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("tsTo", q.TsTo.ToStandard(), clickhouse.NanoSeconds),
	)
	return fmt.Sprintf(sqlFmt, strings.Join(filter, " AND ")), args, true
}

func (c *Client) GetTraceFacetCounts(ctx context.Context, q SpanQuery, field string) ([]FacetValue, error) {
	fromMV := q.DurFrom == 0 && q.DurTo == 0 && !q.Errors && c.useTracesHistogram(ctx, q, q.TsFrom)
	if field == "Namespace" {
		fromMV = false
	}
	query, args, ok := buildTraceFacetQuery(q, field, fromMV)
	if !ok {
		return nil, fmt.Errorf("unsupported trace facet %q", field)
	}
	rows, err := c.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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

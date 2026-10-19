package clickhouse

import (
	"context"
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/ch"
)

const maxTraceFacetValues = 1000

// traceSourceExpr tells the spans the node-agent made from eBPF (service name is
// the container id, which starts with a slash) from the spans of applications
// instrumented with OpenTelemetry.
const traceSourceExpr = "if(startsWith(ServiceName, '/'), 'agent', 'otel')"

// traceNamespaceExpr is the namespace of the application that served a span:
// what comes before the slash of Server (namespace/name), n/a for an
// application with no namespace (a process of a node, an external service).
const traceNamespaceExpr = "if(position(Server, '/') > 0, splitByChar('/', Server)[1], 'n/a')"

// traceDerivedFieldExpr is the expression of a field that is not a column of
// otel_traces, or of otel_traces_histogram when fromMV.
func traceDerivedFieldExpr(field string, fromMV bool) (string, bool) {
	switch field {
	case "Namespace":
		return traceNamespaceExpr, true
	case "ApiRoute":
		return field, true
	case "Source":
		return traceSourceExpr, true
	case "Status":
		if fromMV {
			return "StatusClass", true
		}
		return ch.TraceStatusClassExpression, true
	default:
		return "", false
	}
}

// traceNamespacePredicate compares the prefix of Server for = and !=, which
// ClickHouse reads through the sorting key, unlike traceNamespaceExpr.
func traceNamespacePredicate(op, name, value string) (string, bool) {
	prefix := fmt.Sprintf("startsWith(Server, concat(@%s, '/'))", name)
	if value == "n/a" {
		prefix = "position(Server, '/') = 0"
	}
	switch op {
	case "=":
		return prefix, true
	case "!=":
		return "NOT " + prefix, true
	}
	return derivedFieldPredicate(traceNamespaceExpr, op, name)
}

func derivedFieldPredicate(base, op, name string) (string, bool) {
	switch op {
	case "=":
		return fmt.Sprintf("(%s) = @%s", base, name), true
	case "!=":
		return fmt.Sprintf("NOT ((%s) = @%s)", base, name), true
	case "~":
		return fmt.Sprintf("match(%s, @%s)", base, name), true
	case "!~":
		return fmt.Sprintf("NOT match(%s, @%s)", base, name), true
	default:
		return "", false
	}
}

func traceFacetSelectSQL(field string, fromMV bool) (string, bool) {
	if expr, ok := traceDerivedFieldExpr(field, false); ok {
		if fromMV {
			return "", false
		}
		return fmt.Sprintf(
			"SELECT %s AS %s, count(1) FROM @@table_otel_traces@@ WHERE %%s GROUP BY 1 HAVING %s != '' ORDER BY 2 DESC, 1 LIMIT %d",
			expr, field, field, maxTraceFacetValues,
		), true
	}
	switch field {
	case "ServiceName", "SpanName", "Client", "Server":
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
	// [tsFrom, tsTo), as the charts and the list count it
	filter = append(filter, "Timestamp >= @tsFrom AND Timestamp < @tsTo")
	if !fromMV {
		durFilter, durArgs := q.SelectionFilter()
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
	// Facets always count otel_traces. The latency histogram MV is minute-truncated
	// and has no Namespace/ApiRoute; using it for ServiceName/SpanName made those
	// counts jump when a derived filter disabled the MV.
	query, args, ok := buildTraceFacetQuery(q, field, false)
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

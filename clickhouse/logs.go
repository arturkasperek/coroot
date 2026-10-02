package clickhouse

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"github.com/coroot/coroot/utils"
)

func (c *Client) GetServicesFromLogs(ctx context.Context, from timeseries.Time) ([]string, error) {
	rows, err := c.Query(ctx, "SELECT DISTINCT ServiceName FROM @@table_otel_logs_service_name_severity_text@@ WHERE LastSeen >= @from",
		clickhouse.DateNamed("from", from.ToStandard(), clickhouse.NanoSeconds),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []string
	var app string
	for rows.Next() {
		if err = rows.Scan(&app); err != nil {
			return nil, err
		}
		res = append(res, app)
	}
	return res, nil
}

func (c *Client) GetLogSources(ctx context.Context, from timeseries.Time) (otelServices []string, agentLogsFound bool, err error) {
	services, err := c.GetServicesFromLogs(ctx, from)
	if err != nil {
		return nil, false, err
	}
	for _, s := range services {
		if strings.HasPrefix(s, "/") {
			agentLogsFound = true
		} else {
			otelServices = append(otelServices, s)
		}
	}
	return otelServices, agentLogsFound, nil
}

// GetLogsHistogram is answered by the per-minute rollup unless the query needs
// raw rows (message text, trace id, attributes other than host.name) or the
// buckets do not line up with whole minutes.
func (c *Client) GetLogsHistogram(ctx context.Context, query LogQuery) ([]model.LogHistogramBucket, error) {
	if where, args, ok := query.rollupFilters(nil); ok && query.Ctx.Step >= 60 && query.Ctx.Step%60 == 0 {
		q := fmt.Sprintf("SELECT multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1), toStartOfInterval(Minute, INTERVAL %d second), sum(Count)", query.Ctx.Step)
		q += " FROM @@table_otel_logs_rollup@@ WHERE " + strings.Join(where, " AND ") + " GROUP BY 1, 2"
		return c.queryLogsHistogram(ctx, query, q, args)
	}
	q, args := rawLogsHistogramSQL(query)
	return c.queryLogsHistogram(ctx, query, q, args)
}

func rawLogsHistogramSQL(query LogQuery) (string, []any) {
	where, args := query.filters(nil)
	q := fmt.Sprintf("SELECT multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1), toStartOfInterval(Timestamp, INTERVAL %d second), count(1)", query.Ctx.Step)
	q += " FROM @@table_otel_logs@@ WHERE " + strings.Join(where, " AND ") + " GROUP BY 1, 2"
	return q, args
}

func (c *Client) queryLogsHistogram(ctx context.Context, query LogQuery, q string, args []any) ([]model.LogHistogramBucket, error) {
	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bySeverity := map[int64]*timeseries.TimeSeries{}
	var sev int64
	var t time.Time
	var count uint64
	for rows.Next() {
		if err = rows.Scan(&sev, &t, &count); err != nil {
			return nil, err
		}
		if bySeverity[sev] == nil {
			bySeverity[sev] = timeseries.New(query.Ctx.From, query.Ctx.PointsCount(), query.Ctx.Step)
		}
		bySeverity[sev].Set(timeseries.Time(t.Unix()), float32(count))
	}
	res := make([]model.LogHistogramBucket, 0, len(bySeverity))
	for s, ts := range bySeverity {
		res = append(res, model.LogHistogramBucket{Severity: model.Severity(s), Timeseries: ts})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Severity < res[j].Severity })
	return res, nil
}

func (c *Client) GetLogs(ctx context.Context, query LogQuery) ([]*model.LogEntry, error) {
	where, args := query.filters(nil)
	cond := strings.Join(where, " AND ")
	limit := fmt.Sprint(query.Limit)
	q := logsListSQL(LogListOrderBy(query.Sort), cond, limit)

	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.LogEntry
	for rows.Next() {
		var e model.LogEntry
		var sev int64
		if err = rows.Scan(&e.ServiceName, &e.Timestamp, &sev, &e.Body, &e.TraceId, &e.ResourceAttributes, &e.LogAttributes); err != nil {
			return nil, err
		}
		e.Severity = model.Severity(sev)
		e.ClusterId = c.project.ClusterId()
		e.ClusterName = c.project.Name
		res = append(res, &e)
	}
	return res, nil
}

const maxLogFilterScanWindow = 1 * timeseries.Hour

func (c *Client) GetLogFilters(ctx context.Context, query LogQuery, name string) ([]string, error) {
	if query.Since.IsZero() {
		if window := query.Ctx.To.Sub(query.Ctx.From); window > maxLogFilterScanWindow {
			query.Ctx.From = query.Ctx.To.Add(-maxLogFilterScanWindow)
		}
	}
	where, args := query.filters(&name)
	var q string
	var res []string
	orderBy := "ORDER BY 1"
	settings := " SETTINGS max_block_size=2048, max_threads=4"
	switch name {
	case "":
		res = append(res, "Severity", "Source", "Namespace", "Application", "Message", "Cluster")
		q = "SELECT arrayJoin(arrayConcat(mapKeys(LogAttributes), mapKeys(ResourceAttributes))) AS k"
		orderBy = `GROUP BY 1 HAVING NOT match(k, '\\.\\d+(\\.|$)') ORDER BY count(1) DESC, 1`
	case "Severity":
		q = "SELECT DISTINCT multiIf(SeverityNumber=0, 0, intDiv(SeverityNumber, 4)+1)"
	case "Message":
		return res, nil
	case "Cluster":
		return []string{c.project.Name}, nil
	case "Source":
		return []string{string(model.LogSourceAgent), string(model.LogSourceOtel)}, nil
	case "Namespace", "Application":
		q = "SELECT DISTINCT " + name
	default:
		q = "SELECT DISTINCT arrayJoin([LogAttributes[@attr], ResourceAttributes[@attr]])"
		args = append(args, clickhouse.Named("attr", name))
	}
	q += " FROM @@table_otel_logs@@"
	q += " WHERE " + strings.Join(where, " AND ")
	q += " " + orderBy + " LIMIT 1000" + settings
	rows, err := c.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var s string
	var i int64
	for rows.Next() {
		switch name {
		case "Severity":
			err = rows.Scan(&i)
			s = model.Severity(i).String()
		default:
			err = rows.Scan(&s)
		}
		if err != nil {
			return nil, err
		}
		if s == "" {
			continue
		}
		res = append(res, s)
	}
	return res, nil
}

func (c *Client) GetKubernetesEvents(ctx context.Context, from, to timeseries.Time, limit int, extraFilters ...LogFilter) ([]*model.LogEntry, error) {
	filters := []LogFilter{{Name: "service.name", Op: "=", Value: "KubernetesEvents"}}
	filters = append(filters, extraFilters...)
	q := LogQuery{
		Ctx:     timeseries.NewContext(from, to, 0),
		Filters: filters,
		Limit:   limit,
	}
	return c.GetLogs(ctx, q)
}

type LogQuery struct {
	Ctx      timeseries.Context
	Source   model.LogSource
	Services []string
	Filters  []LogFilter
	Limit    int
	Since    time.Time
	Sort     Sort
}

type LogFilter struct {
	Name  string `json:"name"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

func (lf *LogFilter) Matches(value string) bool {
	if lf == nil {
		return true
	}
	switch lf.Op {
	case "=":
		return value == lf.Value
	case "!=":
		return value != lf.Value
	case "~":
		m, _ := regexp.MatchString(lf.Value, value)
		return m
	case "!~":
		m, _ := regexp.MatchString(lf.Value, value)
		return !m

	}
	return false
}

func (q LogQuery) filters(attr *string) ([]string, []any) {
	where, args, _ := q.buildFilters(attr, false)
	return where, args
}

// rollupFilters is filters for otel_logs_rollup, the per-minute counts. ok is
// false when the rollup cannot give exactly the answer the raw table gives:
// the rollup has no message text, trace ids or arbitrary attributes, only
// whole minutes, and no "newer than" mode.
func (q LogQuery) rollupFilters(attr *string) (where []string, args []any, ok bool) {
	return q.buildFilters(attr, true)
}

func (q LogQuery) buildFilters(attr *string, rollup bool) ([]string, []any, bool) {
	var where []string
	var args []any

	switch len(q.Services) {
	case 0:
		switch q.Source {
		case model.LogSourceAgent:
			where = append(where, "startsWith(ServiceName, '/')")
		case model.LogSourceOtel:
			where = append(where, "NOT startsWith(ServiceName, '/')")
		}
	case 1:
		where = append(where, "ServiceName = @serviceName")
		args = append(args, clickhouse.Named("serviceName", q.Services[0]))
	default:
		where = append(where, "ServiceName IN (@serviceName)")
		args = append(args, clickhouse.Named("serviceName", q.Services))
	}

	if rollup {
		from, to := q.Ctx.From.ToStandard(), q.Ctx.To.ToStandard()
		// Whole minutes only, so a window that starts or ends inside a minute
		// would count the rest of that minute. (Raw rows stamped exactly at the
		// end instant are not counted either; that is one nanosecond.)
		if !q.Since.IsZero() || from.Unix()%60 != 0 || to.Unix()%60 != 0 {
			return nil, nil, false
		}
		where = append(where, "Minute >= toDateTime(@from) AND Minute < toDateTime(@to)")
		args = append(args,
			clickhouse.DateNamed("from", from, clickhouse.NanoSeconds),
			clickhouse.DateNamed("to", to, clickhouse.NanoSeconds),
		)
	} else if !q.Since.IsZero() {
		where = append(where, "Timestamp > @since")
		args = append(args,
			clickhouse.DateNamed("since", q.Since, clickhouse.NanoSeconds),
		)
	} else {
		where = append(where, "Timestamp BETWEEN @from AND @to")
		args = append(args,
			clickhouse.DateNamed("from", q.Ctx.From.ToStandard(), clickhouse.NanoSeconds),
			clickhouse.DateNamed("to", q.Ctx.To.ToStandard(), clickhouse.NanoSeconds),
		)
	}

	filters := utils.Uniq(q.Filters)
	var message messageSearch
	var notMessage []messageSearch
	byName := map[string][]LogFilter{}
	for _, f := range filters {
		if f.Name == "Message" {
			terms := parseMessageSearch(f.Value)
			if rollup && !terms.empty() {
				return nil, nil, false
			}
			if f.Op == "not contains" {
				if !terms.empty() {
					notMessage = append(notMessage, terms)
				}
			} else {
				message.tokens = append(message.tokens, terms.tokens...)
				message.substrings = append(message.substrings, terms.substrings...)
			}
			continue
		}
		if attr != nil && f.Name == *attr {
			continue
		}
		byName[f.Name] = append(byName[f.Name], f)
	}

	i := 0
	for name, attrs := range byName {
		var ors, ands []string
		switch name {
		case "Severity":
			for j, a := range attrs {
				r1, r2 := model.SeverityFromString(a.Value).Range()
				var f *[]string
				var expr string
				switch a.Op {
				case "=":
					expr = "SeverityNumber BETWEEN @%[1]s AND @%[2]s"
					f = &ors
				case "!=":
					expr = "SeverityNumber NOT BETWEEN @%[1]s AND @%[2]s"
					f = &ands
				default:
					continue
				}
				v1 := fmt.Sprintf("severity_from_%d", j)
				v2 := fmt.Sprintf("severity_to_%d", j)
				*f = append(*f, fmt.Sprintf(expr, v1, v2))
				args = append(args, clickhouse.Named(v1, r1))
				args = append(args, clickhouse.Named(v2, r2))
			}
		case "TraceId":
			if rollup {
				return nil, nil, false
			}
			for j, a := range attrs {
				var f *[]string
				var expr string
				switch a.Op {
				case "=":
					expr = "TraceId = @%[1]s"
					f = &ors
				default:
					continue
				}
				v := fmt.Sprintf("trace_id_%d", j)
				*f = append(*f, fmt.Sprintf(expr, v))
				args = append(args, clickhouse.Named(v, a.Value))
			}
		case "service.name":
			for j, a := range attrs {
				var f *[]string
				var expr string
				switch a.Op {
				case "=":
					expr = "ServiceName = @%[1]s"
					f = &ors
				case "!=":
					expr = "ServiceName != @%[1]s"
					f = &ands
				case "~":
					expr = "match(ServiceName, @%[1]s)"
					f = &ors
				case "!~":
					expr = "NOT match(ServiceName, @%[1]s)"
					f = &ands
				default:
					continue
				}
				v := fmt.Sprintf("service_name_%d_%d", i, j)
				*f = append(*f, fmt.Sprintf(expr, v))
				args = append(args, clickhouse.Named(v, a.Value))
			}
		case "Source":
			for _, a := range attrs {
				var expr string
				switch a.Value {
				case string(model.LogSourceAgent):
					expr = "startsWith(ServiceName, '/')"
				case string(model.LogSourceOtel):
					expr = "NOT startsWith(ServiceName, '/')"
				default:
					continue
				}
				switch a.Op {
				case "=":
					ors = append(ors, expr)
				case "!=":
					ands = append(ands, "NOT ("+expr+")")
				}
			}
		case "Namespace", "Application":
			for j, a := range attrs {
				var f *[]string
				var expr string
				switch a.Op {
				case "=":
					expr = name + " = @%[1]s"
					f = &ors
				case "!=":
					expr = name + " != @%[1]s"
					f = &ands
				default:
					continue
				}
				v := fmt.Sprintf("derived_%s_%d_%d", name, i, j)
				*f = append(*f, fmt.Sprintf(expr, v))
				args = append(args, clickhouse.Named(v, a.Value))
			}
		default:
			if rollup {
				// the rollup keeps one attribute, host.name, in HostLog/HostRes
				if name != "host.name" {
					return nil, nil, false
				}
				for j, a := range attrs {
					var f *[]string
					var expr string
					switch a.Op {
					case "=":
						expr = "(HostLog = @%[1]s OR HostRes = @%[1]s)"
						f = &ors
					case "!=":
						expr = "(HostLog != @%[1]s AND HostRes != @%[1]s)"
						f = &ands
					case "~":
						expr = "(match(HostLog, @%[1]s) OR match(HostRes, @%[1]s))"
						f = &ors
					case "!~":
						expr = "(NOT match(HostLog, @%[1]s) AND NOT match(HostRes, @%[1]s))"
						f = &ands
					default:
						continue
					}
					v := fmt.Sprintf("attr_values_%d_%d", i, j)
					*f = append(*f, fmt.Sprintf(expr, v))
					args = append(args, clickhouse.Named(v, a.Value))
				}
				break
			}
			for j, a := range attrs {
				var f *[]string
				var expr string
				switch a.Op {
				case "=":
					expr = "(LogAttributes[@%[1]s] = @%[2]s OR ResourceAttributes[@%[1]s] = @%[2]s)"
					f = &ors
				case "!=":
					expr = "(LogAttributes[@%[1]s] != @%[2]s AND ResourceAttributes[@%[1]s] != @%[2]s)"
					f = &ands
				case "~":
					expr = "(match(LogAttributes[@%[1]s], @%[2]s) OR match(ResourceAttributes[@%[1]s], @%[2]s))"
					f = &ors
				case "!~":
					expr = "(NOT match(LogAttributes[@%[1]s], @%[2]s) AND NOT match(ResourceAttributes[@%[1]s], @%[2]s))"
					f = &ands
				default:
					continue
				}
				n := fmt.Sprintf("attr_name_%d_%d", i, j)
				v := fmt.Sprintf("attr_values_%d_%d", i, j)
				*f = append(*f, fmt.Sprintf(expr, n, v))
				args = append(args, clickhouse.Named(n, name))
				args = append(args, clickhouse.Named(v, a.Value))
			}
		}
		if len(ands) > 0 {
			where = append(where, "("+strings.Join(ands, " AND ")+")")
		}
		if len(ors) > 0 {
			where = append(where, "("+strings.Join(ors, " OR ")+")")
		}
		i++
	}

	if expr := messageExpr(message.dedup(), "token", &args); expr != "" {
		where = append(where, expr)
	}
	for k, terms := range notMessage {
		if expr := messageExpr(terms.dedup(), fmt.Sprintf("not_token_%d", k), &args); expr != "" {
			where = append(where, fmt.Sprintf("NOT (%s)", expr))
		}
	}

	return where, args, true
}

// messageSearch is what a Message filter asks for: words that must all occur
// as whole words, and substrings that must occur anywhere.
type messageSearch struct {
	tokens     []string
	substrings []string
}

func (m messageSearch) empty() bool {
	return len(m.tokens) == 0 && len(m.substrings) == 0
}

// dedup drops repeats and keeps the order, so the same search always produces
// the same query text and arguments.
func (m messageSearch) dedup() messageSearch {
	uniq := func(in []string) []string {
		seen := map[string]bool{}
		var out []string
		for _, s := range in {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		return out
	}
	return messageSearch{tokens: uniq(m.tokens), substrings: uniq(m.substrings)}
}

// parseMessageSearch splits the search box text. A word is lowercased and cut
// at spaces and ASCII punctuation, which is how the text index on
// lowerUTF8(Body) tokenizes the message (splitByNonAlpha), so "db-5432" is the
// words "db" and "5432". A term that starts or ends with * (fail*, *eout) is a
// substring search instead: it can match inside a word, but it cannot use the
// index and reads every message in the time range.
func parseMessageSearch(value string) messageSearch {
	var res messageSearch
	for _, term := range strings.Fields(value) {
		if strings.HasPrefix(term, "*") || strings.HasSuffix(term, "*") {
			if sub := strings.Trim(term, "*"); sub != "" {
				res.substrings = append(res.substrings, sub)
			}
			continue
		}
		tokens := strings.FieldsFunc(term, func(r rune) bool {
			return unicode.IsSpace(r) || (r <= unicode.MaxASCII && !unicode.IsNumber(r) && !unicode.IsLetter(r))
		})
		for _, tok := range tokens {
			res.tokens = append(res.tokens, strings.ToLower(tok))
		}
	}
	return res
}

// messageExpr builds the WHERE part for a search. The words become one
// hasAllTokens over lowerUTF8(Body): ClickHouse serves it from the text index
// only when the expression is the same as the index's, so keep them in step
// with idx_body in ch/client.go.
func messageExpr(m messageSearch, prefix string, args *[]any) string {
	var ands []string
	if len(m.tokens) > 0 {
		name := prefix + "_tokens"
		ands = append(ands, fmt.Sprintf("hasAllTokens(lowerUTF8(Body), @%s)", name))
		*args = append(*args, clickhouse.Named(name, m.tokens))
	}
	for i, sub := range m.substrings {
		name := fmt.Sprintf("%s_sub_%d", prefix, i)
		ands = append(ands, fmt.Sprintf("positionCaseInsensitiveUTF8(Body, @%s) > 0", name))
		*args = append(*args, clickhouse.Named(name, sub))
	}
	return strings.Join(ands, " AND ")
}

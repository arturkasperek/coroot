package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/coroot/coroot/ch"
	"github.com/coroot/coroot/db"
	"github.com/coroot/coroot/model"
	"github.com/coroot/coroot/timeseries"
	"golang.org/x/exp/maps"
	"k8s.io/klog"
)

var (
	HistogramBuckets    = []float64{0, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, math.Inf(1)}
	HistogramNextBucket = map[float32]float32{}
)

func init() {
	for i, b := range HistogramBuckets[:len(HistogramBuckets)-2] {
		HistogramNextBucket[float32(b)] = float32(HistogramBuckets[i+1])
	}
}

func (c *Client) GetServicesFromTraces(ctx context.Context, from timeseries.Time) ([]string, error) {
	rows, err := c.Query(ctx, "SELECT DISTINCT ServiceName FROM @@table_otel_traces_service_name@@ WHERE LastSeen >= @from",
		clickhouse.DateNamed("from", from.ToStandard(), clickhouse.NanoSeconds),
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	var res []string
	for rows.Next() {
		var app string
		if err = rows.Scan(&app); err != nil {
			return nil, err
		}
		res = append(res, app)
	}
	return res, nil
}

// RootSpansCounts counts root spans per time bucket of the chart, by status
// class and by duration bucket.
type RootSpansCounts struct {
	// ByStatusClass holds the number of spans of each status class.
	ByStatusClass map[string]*timeseries.TimeSeries
	// ByDurationBucket holds the number of spans keyed by the lower bound of the
	// duration bucket in milliseconds, one of HistogramBuckets.
	ByDurationBucket map[float64]*timeseries.TimeSeries
}

// GetRootSpansCounts reads the counts of the Traces page charts: the window
// and the filters of the query, not its selection.
func (c *Client) GetRootSpansCounts(ctx context.Context, q SpanQuery, window TracesWindow) (*RootSpansCounts, error) {
	fromMV := window.minuteAligned() && c.useTracesHistogram(ctx, q, window.From)
	filters, filterArgs := q.RootSpansFilter(fromMV)

	from, step, to := window.From, window.Step, window.To
	points := window.Points()

	filters = append(filters, "Timestamp >= @from AND Timestamp < @to")
	filterArgs = append(filterArgs,
		clickhouse.Named("step", int(step)),
		clickhouse.DateNamed("from", from.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("to", to.ToStandard(), clickhouse.NanoSeconds),
	)
	var query string
	if fromMV {
		query = "SELECT toStartOfInterval(Timestamp, INTERVAL @step second), StatusClass, Bucket, sum(Total)"
		query += " FROM @@table_otel_traces_histogram@@"
	} else {
		filterArgs = append(filterArgs, clickhouse.Named("buckets", HistogramBuckets[:len(HistogramBuckets)-1]))
		query = "SELECT toStartOfInterval(Timestamp, INTERVAL @step second), " + ch.TraceStatusClassExpression + ", roundDown(Duration/1000000, @buckets), count(1)"
		query += " FROM @@table_otel_traces@@"
	}
	query += " WHERE " + strings.Join(filters, " AND ")
	query += " GROUP BY 1, 2, 3"

	rows, err := c.Query(ctx, query, filterArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byStatusClass := map[string][]float32{}
	byDurationBucket := map[float64][]float32{}
	var timestamp time.Time
	var statusClass string
	var bucket float64
	var total uint64
	for rows.Next() {
		if err = rows.Scan(&timestamp, &statusClass, &bucket, &total); err != nil {
			return nil, err
		}
		index := int(timeseries.Time(timestamp.Unix()).Sub(from) / step)
		if index < 0 || index >= points {
			continue
		}
		addCount(byStatusClass, statusClass, points, index, total)
		addCount(byDurationBucket, bucket, points, index, total)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	res := &RootSpansCounts{
		ByStatusClass:    map[string]*timeseries.TimeSeries{},
		ByDurationBucket: map[float64]*timeseries.TimeSeries{},
	}
	for class, data := range byStatusClass {
		res.ByStatusClass[class] = timeseries.NewWithData(from, step, data)
	}
	for bucket, data := range byDurationBucket {
		res.ByDurationBucket[bucket] = timeseries.NewWithData(from, step, data)
	}
	return res, nil
}

// addCount adds count to the point at index of counts[key], creating the
// points of the key, NaN where nothing is counted, on its first count.
func addCount[K comparable](counts map[K][]float32, key K, points, index int, count uint64) {
	data := counts[key]
	if data == nil {
		data = make([]float32, points)
		for i := range data {
			data[i] = timeseries.NaN
		}
		counts[key] = data
	}
	if timeseries.IsNaN(data[index]) {
		data[index] = 0
	}
	data[index] += float32(count)
}

// chartStep is the step of a chart read from the histogram table, whose rows
// are per minute: the step of the view rounded up to whole minutes.
func chartStep(step timeseries.Duration) timeseries.Duration {
	if remainder := step % 60; remainder != 0 {
		step += 60 - remainder
	}
	return step
}

// tracesWindowFineSteps are the steps of a view of up to an hour, read from
// otel_traces: the smallest that gives at most tracesWindowMaxPoints buckets.
var tracesWindowFineSteps = []timeseries.Duration{1, 2, 5, 10, 15, 30, 60}

const tracesWindowMaxPoints = 60

// TracesWindow is the time range of the Traces page, which its charts, facets
// and list all count, so that their numbers add up: from the start of the
// bucket of the view's start, to now for a live view (its last bucket is the
// one in progress) or to the end of the bucket of the view's end for a view
// in the past (every bucket is whole). To is exclusive.
type TracesWindow struct {
	From timeseries.Time
	To   timeseries.Time
	Step timeseries.Duration
}

// NewTracesWindow makes the window of a view from from to to, live when to is
// now. A view of up to an hour gets a step of seconds, so that its last 15
// seconds are its last 15 seconds; a longer one the step of the view
// (viewStep) rounded up to whole minutes, the rows of the histogram table.
func NewTracesWindow(from, to timeseries.Time, live bool, viewStep timeseries.Duration) TracesWindow {
	duration := to.Sub(from)
	step := chartStep(viewStep)
	if duration <= timeseries.Hour {
		for _, fine := range tracesWindowFineSteps {
			step = fine
			if duration <= fine*tracesWindowMaxPoints {
				break
			}
		}
	}
	w := TracesWindow{From: from.Truncate(step), To: to, Step: step}
	if !live {
		if aligned := to.Truncate(step); aligned != to {
			w.To = aligned.Add(step)
		}
	}
	return w
}

// Points is the number of buckets of the window, the last one in progress for
// a live view.
func (w TracesWindow) Points() int {
	return int((w.To.Sub(w.From) + w.Step - 1) / w.Step)
}

// ChartContext is the time axis of the charts of the window: a point per
// bucket, at its start.
func (w TracesWindow) ChartContext() timeseries.Context {
	return timeseries.Context{From: w.From, To: w.From.Add(timeseries.Duration(w.Points()-1) * w.Step), Step: w.Step}
}

// minuteAligned tells whether the window can be read from the histogram
// table, whose rows are per minute: its step and its start are whole minutes.
// Its end may be any time: the rows of the minute in progress hold its spans
// up to now.
func (w TracesWindow) minuteAligned() bool {
	return w.Step%60 == 0 && w.From%60 == 0
}

func (c *Client) GetRootSpans(ctx context.Context, q SpanQuery) ([]*model.TraceSpan, error) {
	filter, filterArgs := q.RootSpansFilter(false)
	return c.getSpans(ctx, q, TraceListOrderBy(q.Sort), filter, filterArgs)
}

func (c *Client) GetTraceSpanStats(ctx context.Context, q SpanQuery) (map[model.TraceSpanKey]*model.TraceSpanStats, error) {
	fromMV := q.DurFrom == 0 && q.DurTo == 0 && !q.Errors && c.useTracesHistogram(ctx, q, q.TsFrom)
	filter, filterArgs := q.RootSpansFilter(fromMV)
	return c.getTraceSpanStats(ctx, q, filter, filterArgs, fromMV)
}

func (c *Client) GetTraceErrors(ctx context.Context, q SpanQuery) (map[model.TraceSpanKey]*model.TraceErrorsStat, error) {
	return c.getTraceErrors(ctx, q)
}

func (c *Client) GetSpansByServiceNameHistogram(ctx context.Context, q SpanQuery) ([]model.HistogramBucket, error) {
	fromMV := c.useTracesHistogram(ctx, q, q.Ctx.From)
	filter, filterArgs := q.SpansByServiceNameFilter(fromMV)
	return c.getSpansHistogram(ctx, q, filter, filterArgs, fromMV)
}

func (c *Client) GetSpansByServiceName(ctx context.Context, q SpanQuery) ([]*model.TraceSpan, error) {
	filter, filterArgs := q.SpansByServiceNameFilter(false)
	return c.getSpans(ctx, q, "", filter, filterArgs)
}

func (c *Client) GetInboundSpansHistogram(ctx context.Context, q SpanQuery, clients []string, listens []model.Listen) ([]model.HistogramBucket, error) {
	if len(listens) == 0 {
		return nil, nil
	}
	fromMV := c.useTracesHistogram(ctx, q, q.Ctx.From)
	filter, filterArgs := inboundSpansFilter(clients, listens, fromMV)
	return c.getSpansHistogram(ctx, q, filter, filterArgs, fromMV)
}

func (c *Client) GetInboundSpans(ctx context.Context, q SpanQuery, clients []string, listens []model.Listen) ([]*model.TraceSpan, error) {
	if len(listens) == 0 {
		return nil, nil
	}
	filter, filterArgs := inboundSpansFilter(clients, listens, false)
	return c.getSpans(ctx, q, "", filter, filterArgs)
}

func (c *Client) GetParentSpans(ctx context.Context, spans []*model.TraceSpan) ([]*model.TraceSpan, error) {
	traceIds := map[string]bool{}
	var ids []clickhouse.GroupSet
	for _, s := range spans {
		if s.ParentSpanId != "" {
			ids = append(ids, clickhouse.GroupSet{Value: []any{s.TraceId, s.ParentSpanId}})
			traceIds[s.TraceId] = true
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var minTs, maxTs time.Time
	err := c.QueryRow(ctx,
		"SELECT min(Start), max(End)+1 FROM @@table_otel_traces_trace_id_ts@@ WHERE TraceId IN (@traceIds)",
		clickhouse.Named("traceIds", maps.Keys(traceIds)),
	).Scan(&minTs, &maxTs)
	if err != nil {
		return nil, err
	}
	q := SpanQuery{
		TsFrom: timeseries.TimeFromStandard(minTs),
		TsTo:   timeseries.TimeFromStandard(maxTs),
	}
	return c.getSpans(ctx, q, "",
		[]string{"TraceId IN (@traceIds)", "(TraceId, SpanId) IN (@ids)"},
		[]any{
			clickhouse.Named("traceIds", maps.Keys(traceIds)),
			clickhouse.Named("ids", ids),
		},
	)
}

// GetSpansByTraceId loads one trace. withBodies adds the HTTP bodies, which only
// the single-trace view shows.
func (c *Client) GetSpansByTraceId(ctx context.Context, traceId string, withBodies bool) ([]*model.TraceSpan, error) {
	var minTs, maxTs time.Time
	err := c.QueryRow(ctx,
		"SELECT min(Start), max(End)+1 FROM @@table_otel_traces_trace_id_ts@@ WHERE TraceId = @traceId",
		clickhouse.Named("traceId", traceId),
	).Scan(&minTs, &maxTs)
	if err != nil {
		return nil, err
	}
	q := SpanQuery{
		TsFrom:     timeseries.TimeFromStandard(minTs),
		TsTo:       timeseries.TimeFromStandard(maxTs),
		WithBodies: withBodies,
	}
	return c.getSpans(ctx, q, "Timestamp",
		[]string{"TraceId = @traceId"},
		[]any{
			clickhouse.Named("traceId", traceId),
		},
	)
}

func (c *Client) getOtelTracesServiceName(ctx context.Context, world *model.World, app *model.Application) (string, error) {
	if app.Settings != nil && app.Settings.Tracing != nil {
		return app.Settings.Tracing.Service, nil
	}
	services, err := c.GetServicesFromTraces(ctx, world.Ctx.From)
	if err != nil {
		return "", err
	}
	var otelServices []string
	for _, s := range services {
		if !strings.HasPrefix(s, "/") {
			otelServices = append(otelServices, s)
		}
	}
	return model.GuessService(otelServices, world, app), nil
}

func (c *Client) GetTracesViolatingSLOs(ctx context.Context, from, to timeseries.Time, world *model.World, app *model.Application) (*model.Trace, *model.Trace, error) {
	serviceName, err := c.getOtelTracesServiceName(ctx, world, app)
	if err != nil || serviceName == "" {
		return nil, nil, err
	}

	sq := SpanQuery{
		Ctx:    world.Ctx,
		TsFrom: from,
		TsTo:   to,
		Limit:  1,
	}

	sq.AddFilter("ServiceName", "=", serviceName)

	sq.Errors = true
	var errorTrace, slowTrace *model.Trace

	if errorTrace, err = c.getTrace(ctx, sq); err != nil {
		return nil, nil, err
	}

	if len(app.LatencySLIs) > 0 && app.LatencySLIs[0].Config.ObjectivePercentage > 0 {
		sq.Errors = false
		sq.DurFrom = time.Duration(float64(app.LatencySLIs[0].Config.ObjectiveBucket) * float64(time.Second))
		if slowTrace, err = c.getTrace(ctx, sq); err != nil {
			return nil, nil, err
		}
	}

	return errorTrace, slowTrace, nil
}

func (c *Client) getTrace(ctx context.Context, sq SpanQuery) (*model.Trace, error) {
	spans, err := c.GetSpansByServiceName(ctx, sq)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 || spans[0].TraceId == "" {
		return nil, nil
	}
	if spans, err = c.GetSpansByTraceId(ctx, spans[0].TraceId, false); err != nil {
		return nil, err
	}
	return &model.Trace{Spans: spans}, nil
}

func (c *Client) getSpansHistogram(ctx context.Context, q SpanQuery, filters []string, filterArgs []any, fromMV bool) ([]model.HistogramBucket, error) {
	step := chartStep(q.Ctx.Step)
	from := q.Ctx.From
	to := q.Ctx.To.Add(step)

	filters = append(filters, "Timestamp BETWEEN @from AND @to")
	filterArgs = append(filterArgs,
		clickhouse.Named("step", int(step)),
		clickhouse.DateNamed("from", from.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("to", to.ToStandard(), clickhouse.NanoSeconds),
	)

	var query string
	if fromMV {
		query = "SELECT toStartOfInterval(Timestamp, INTERVAL @step second), Bucket, sum(Total), sum(Failed)"
		query += " FROM @@table_otel_traces_histogram@@"
	} else {
		filterArgs = append(filterArgs, clickhouse.Named("buckets", HistogramBuckets[:len(HistogramBuckets)-1]))
		query = "SELECT toStartOfInterval(Timestamp, INTERVAL @step second), roundDown(Duration/1000000, @buckets), count(1), countIf(StatusCode = 'STATUS_CODE_ERROR')"
		query += " FROM @@table_otel_traces@@"
	}
	query += " WHERE " + strings.Join(filters, " AND ")
	query += " GROUP BY 1, 2"

	rows, err := c.Query(ctx, query, filterArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var t time.Time
	var bucket float64
	var total, failed uint64
	byBucket := map[float64]*timeseries.TimeSeries{}
	errors := map[timeseries.Time]uint64{}
	for rows.Next() {
		if err = rows.Scan(&t, &bucket, &total, &failed); err != nil {
			return nil, err
		}
		if byBucket[bucket] == nil {
			byBucket[bucket] = timeseries.New(from, int(to.Sub(from)/step), step)
		}
		ts := timeseries.Time(t.Unix())
		byBucket[bucket].Set(ts, float32(total)/float32(step))
		errors[ts] += failed
	}

	if len(byBucket) == 0 {
		return nil, nil
	}

	res := []model.HistogramBucket{
		{TimeSeries: timeseries.New(from, int(to.Sub(from)/step), step)}, // errors
	}
	for ts, count := range errors {
		res[0].TimeSeries.Set(ts, float32(count)/float32(step))
	}
	for i := 1; i < len(HistogramBuckets); i++ {
		ts := byBucket[HistogramBuckets[i-1]]
		if ts.IsEmpty() {
			ts = timeseries.New(from, int(to.Sub(from)/step), step)
		}
		if len(res) > 0 {
			ts = timeseries.Aggregate2(res[len(res)-1].TimeSeries, ts, func(x, y float32) float32 {
				if timeseries.IsNaN(x) {
					return y
				}
				if timeseries.IsNaN(y) {
					return x
				}
				return x + y
			})
		}
		res = append(res, model.HistogramBucket{
			Le:         float32(HistogramBuckets[i] / 1000),
			TimeSeries: ts,
		})
	}
	return res, nil
}

func (c *Client) getTraceSpanStats(ctx context.Context, q SpanQuery, filters []string, filterArgs []any, fromMV bool) (map[model.TraceSpanKey]*model.TraceSpanStats, error) {
	if fromMV {
		filters = append(filters, "Timestamp >= @tsFrom AND Timestamp < @tsTo")
	} else {
		filters = append(filters, "Timestamp BETWEEN @tsFrom AND @tsTo")
	}
	filterArgs = append(filterArgs,
		clickhouse.DateNamed("tsFrom", q.TsFrom.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("tsTo", q.TsTo.ToStandard(), clickhouse.NanoSeconds),
	)

	var query string
	if fromMV {
		query = "SELECT ServiceName, SpanName, Bucket, sum(Total), sum(Failed)"
		query += " FROM @@table_otel_traces_histogram@@"
	} else {
		filterArgs = append(filterArgs, clickhouse.Named("buckets", HistogramBuckets[:len(HistogramBuckets)-1]))
		durFilter, durFilterArgs := q.SelectionFilter()
		if durFilter != "" {
			filters = append(filters, durFilter)
			filterArgs = append(filterArgs, durFilterArgs...)
		}
		query = "SELECT ServiceName, SpanName, roundDown(Duration/1000000, @buckets), count(1), countIf(StatusCode = 'STATUS_CODE_ERROR')"
		query += " FROM @@table_otel_traces@@"
	}
	query += " WHERE " + strings.Join(filters, " AND ")
	query += " GROUP BY 1, 2, 3"

	rows, err := c.Query(ctx, query, filterArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bucket float64
	var total, failed uint64

	res := map[model.TraceSpanKey]*model.TraceSpanStats{}

	k := model.TraceSpanKey{Cluster: c.Project().Name}
	for rows.Next() {
		if err = rows.Scan(&k.ServiceName, &k.SpanName, &bucket, &total, &failed); err != nil {
			return nil, err
		}
		stats := res[k]
		if stats == nil {
			stats = &model.TraceSpanStats{TraceSpanKey: k, Histogram: map[float32]float32{}}
			res[k] = stats
		}
		stats.Total += float32(total)
		stats.Failed += float32(failed)
		stats.Histogram[float32(bucket)] = float32(total)
	}
	return res, nil
}

var spanSliceWindows = []timeseries.Duration{timeseries.Hour, 2 * timeseries.Hour, 4 * timeseries.Hour}

func (c *Client) getSpans(ctx context.Context, q SpanQuery, orderBy string, filters []string, filterArgs []any) ([]*model.TraceSpan, error) {
	if orderBy != "" || q.Limit <= 0 || q.TsFrom.IsZero() || q.TsTo.IsZero() {
		return c.querySpans(ctx, q, orderBy, filters, filterArgs)
	}
	var res []*model.TraceSpan
	to := q.TsTo
	for _, d := range spanSliceWindows {
		from := to.Add(-d)
		if from <= q.TsFrom {
			break
		}
		sq := q
		sq.TsFrom = from
		sq.TsTo = to
		sq.Limit = q.Limit - len(res)
		spans, err := c.querySpans(ctx, sq, "", append([]string{}, filters...), append([]any{}, filterArgs...))
		if err != nil {
			return nil, err
		}
		res = append(res, spans...)
		if len(res) >= q.Limit {
			return res, nil
		}
		to = from
	}
	sq := q
	sq.TsTo = to
	sq.Limit = q.Limit - len(res)
	spans, err := c.querySpans(ctx, sq, "", filters, filterArgs)
	if err != nil {
		return nil, err
	}
	return append(res, spans...), nil
}

// spanColumns is the SELECT list of every span query. The bodies are in
// trace_bodies, which only the single-trace view reads (loadBodies).
func spanColumns(withBodies bool) string {
	cols := "Timestamp, TraceId, SpanId, ParentSpanId, SpanName, ServiceName, Client, Server, Duration, StatusCode, StatusMessage, ResourceAttributes, SpanAttributes, Events.Timestamp, Events.Name, Events.Attributes"
	if withBodies {
		cols += ", RequestBodyHash, ResponseBodyHash, SpanKind, RequestKey"
	}
	return cols
}

// clientSpanWindow is how far in time from a server span its client span is
// looked for: the clocks of two nodes, and the spans of the one request. A
// 4-tuple, and so a key, is not used again for a minute (TIME_WAIT).
const clientSpanWindow = 5 * time.Second

// borrowClientBodies gives the server spans whose server skipped the bodies
// (the client's agent captured them) the body hashes of their client spans,
// found by RequestKey. A client span counts only with the same method and
// target, within clientSpanWindow: no body rather than another request's.
func (c *Client) borrowClientBodies(ctx context.Context, spans []*model.TraceSpan) error {
	var servers []*model.TraceSpan
	keys := map[uint64]bool{}
	var from, to time.Time
	for _, s := range spans {
		if s.SpanKind != "SPAN_KIND_SERVER" || s.RequestKey == 0 || s.RequestBodyHash != 0 || s.ResponseBodyHash != 0 {
			continue
		}
		servers = append(servers, s)
		keys[s.RequestKey] = true
		if from.IsZero() || s.Timestamp.Before(from) {
			from = s.Timestamp
		}
		if s.Timestamp.After(to) {
			to = s.Timestamp
		}
	}
	if len(servers) == 0 {
		return nil
	}
	rows, err := c.Query(ctx, `
SELECT RequestKey, Timestamp, SpanAttributes['http.method'], SpanAttributes['http.target'], RequestBodyHash, ResponseBodyHash
FROM @@table_otel_traces@@
WHERE RequestKey IN (@keys) AND SpanKind = 'SPAN_KIND_CLIENT' AND Timestamp BETWEEN @from AND @to`,
		clickhouse.Named("keys", maps.Keys(keys)),
		clickhouse.DateNamed("from", from.Add(-clientSpanWindow), clickhouse.NanoSeconds),
		clickhouse.DateNamed("to", to.Add(clientSpanWindow), clickhouse.NanoSeconds),
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	type clientSpan struct {
		timestamp                         time.Time
		method, target                    string
		requestBodyHash, responseBodyHash uint64
	}
	clients := map[uint64][]clientSpan{}
	for rows.Next() {
		var key uint64
		var span clientSpan
		if err = rows.Scan(&key, &span.timestamp, &span.method, &span.target, &span.requestBodyHash, &span.responseBodyHash); err != nil {
			return err
		}
		clients[key] = append(clients[key], span)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, server := range servers {
		var best *clientSpan
		var bestDistance time.Duration
		for index, client := range clients[server.RequestKey] {
			if client.method != server.SpanAttributes["http.method"] || client.target != server.SpanAttributes["http.target"] {
				continue
			}
			distance := client.timestamp.Sub(server.Timestamp)
			if distance < 0 {
				distance = -distance
			}
			if distance > clientSpanWindow {
				continue
			}
			if best == nil || distance < bestDistance {
				best, bestDistance = &clients[server.RequestKey][index], distance
			}
		}
		if best != nil {
			server.RequestBodyHash, server.ResponseBodyHash = best.requestBodyHash, best.responseBodyHash
		}
	}
	return nil
}

// loadBodies fills the bodies of the spans from trace_bodies by their hashes.
// A body may be in several rows (one a day it was written), all the same.
func (c *Client) loadBodies(ctx context.Context, spans []*model.TraceSpan) error {
	hashes := map[uint64]bool{}
	for _, s := range spans {
		for _, hash := range []uint64{s.RequestBodyHash, s.ResponseBodyHash} {
			if hash != 0 {
				hashes[hash] = true
			}
		}
	}
	if len(hashes) == 0 {
		return nil
	}
	rows, err := c.Query(ctx,
		"SELECT Hash, any(Body) FROM @@table_trace_bodies@@ WHERE Hash IN (@hashes) GROUP BY Hash",
		clickhouse.Named("hashes", maps.Keys(hashes)),
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	bodies := map[uint64]string{}
	var hash uint64
	var body string
	for rows.Next() {
		if err = rows.Scan(&hash, &body); err != nil {
			return err
		}
		bodies[hash] = body
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, s := range spans {
		s.RequestBody = bodies[s.RequestBodyHash]
		s.ResponseBody = bodies[s.ResponseBodyHash]
	}
	return nil
}

func (c *Client) querySpans(ctx context.Context, q SpanQuery, orderBy string, filters []string, filterArgs []any) ([]*model.TraceSpan, error) {
	tsFilter := "Timestamp >= @tsFrom AND Timestamp < @tsTo"
	tsFilterArgs := []any{
		clickhouse.DateNamed("tsFrom", q.TsFrom.ToStandard(), clickhouse.NanoSeconds),
		clickhouse.DateNamed("tsTo", q.TsTo.ToStandard(), clickhouse.NanoSeconds),
	}
	if !q.TsFrom.IsZero() && !q.TsTo.IsZero() {
		filters = append(filters, tsFilter)
		filterArgs = append(filterArgs, tsFilterArgs...)
	}
	durFilter, durFilterArgs := q.SelectionFilter()
	if durFilter != "" {
		filters = append(filters, durFilter)
		filterArgs = append(filterArgs, durFilterArgs...)
	}

	cond := strings.Join(filters, " AND ")
	query := "SELECT " + spanColumns(q.WithBodies)
	query += " FROM @@table_otel_traces@@"
	query += " WHERE " + cond
	limit := ""
	if q.Limit > 0 {
		limit = fmt.Sprint(q.Limit)
	}
	extra, orderBy := timestampLimitCutoff(orderBy, "@@table_otel_traces@@", cond, limit)
	if extra != "" {
		query += " AND " + extra
	}
	if orderBy != "" {
		query += " ORDER BY " + orderBy
	}
	if limit != "" {
		query += " LIMIT " + limit
	}

	rows, err := c.Query(ctx, query, filterArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []*model.TraceSpan
	for rows.Next() {
		var s model.TraceSpan
		var eventsTimestamp []time.Time
		var eventsName []string
		var eventsAttributes []map[string]string
		dest := []any{
			&s.Timestamp, &s.TraceId, &s.SpanId, &s.ParentSpanId, &s.Name, &s.ServiceName, &s.Client, &s.Server,
			&s.Duration, &s.StatusCode, &s.StatusMessage,
			&s.ResourceAttributes, &s.SpanAttributes,
			&eventsTimestamp, &eventsName, &eventsAttributes,
		}
		if q.WithBodies {
			dest = append(dest, &s.RequestBodyHash, &s.ResponseBodyHash, &s.SpanKind, &s.RequestKey)
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		l := len(eventsTimestamp)
		if l > 0 && l == len(eventsName) && l == len(eventsAttributes) {
			s.Events = make([]model.TraceSpanEvent, l)
			for i := range eventsTimestamp {
				s.Events[i].Timestamp = eventsTimestamp[i]
				s.Events[i].Name = eventsName[i]
				s.Events[i].Attributes = eventsAttributes[i]
			}
		}
		s.ClusterName = c.Project().Name
		res = append(res, &s)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if q.WithBodies {
		if err = c.borrowClientBodies(ctx, res); err != nil {
			return nil, err
		}
		if err = c.loadBodies(ctx, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (c *Client) getTraces(ctx context.Context, from, to timeseries.Time, filters []string, filterArgs []any) ([]*model.Trace, error) {
	const limit = 200
	cond := strings.Join(filters, " AND ")
	var sliceBounds [][2]timeseries.Time
	sliceTo := to
	for _, d := range spanSliceWindows {
		sliceFrom := sliceTo.Add(-d)
		if sliceFrom <= from {
			break
		}
		sliceBounds = append(sliceBounds, [2]timeseries.Time{sliceFrom, sliceTo})
		sliceTo = sliceFrom
	}
	sliceBounds = append(sliceBounds, [2]timeseries.Time{from, sliceTo})

	var groups [][]string
	seen := map[string]bool{}
	remaining := limit
	for _, b := range sliceBounds {
		query := "SELECT count(1), groupArray(distinct TraceId)"
		query += " FROM (SELECT TraceId FROM @@table_otel_traces@@ WHERE " + cond
		query += " AND Timestamp >= @sliceFrom AND Timestamp < @sliceTo ORDER BY Timestamp DESC LIMIT " + fmt.Sprint(remaining) + ")"
		args := append(append([]any{}, filterArgs...),
			clickhouse.DateNamed("sliceFrom", b[0].ToStandard(), clickhouse.NanoSeconds),
			clickhouse.DateNamed("sliceTo", b[1].ToStandard(), clickhouse.NanoSeconds),
		)
		var cnt uint64
		var ids []string
		t := time.Now()
		if err := c.QueryRow(ctx, query, args...).Scan(&cnt, &ids); err != nil {
			return nil, err
		}
		klog.Infof("trace ids query took %s, returned %d traces: %s, args: %v", time.Since(t).Truncate(time.Millisecond), len(ids), query, args)
		if cnt == 0 {
			continue
		}
		var group []string
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				group = append(group, id)
			}
		}
		if len(group) > 0 {
			groups = append(groups, group)
		}
		remaining -= int(cnt)
		if remaining <= 0 {
			break
		}
	}

	res := map[string]*model.Trace{}
	for _, traceIds := range groups {
		if err := c.getTraceSpans(ctx, from, to, traceIds, res); err != nil {
			return nil, err
		}
	}
	return maps.Values(res), nil
}

func (c *Client) getTraceSpans(ctx context.Context, from, to timeseries.Time, traceIds []string, res map[string]*model.Trace) error {
	var minTs, maxTs time.Time
	err := c.QueryRow(ctx,
		"SELECT min(Start), max(End)+1 FROM @@table_otel_traces_trace_id_ts@@ WHERE TraceId IN (@traceIds)",
		clickhouse.Named("traceIds", traceIds),
	).Scan(&minTs, &maxTs)
	if err != nil {
		return err
	}
	if minTs.IsZero() || minTs.Unix() <= 0 {
		minTs, maxTs = from.ToStandard(), to.ToStandard().Add(time.Second)
	}
	query := "SELECT " + spanColumns(false) + `
FROM @@table_otel_traces@@
WHERE Timestamp BETWEEN @from AND @to AND TraceId IN @traceIds`
	rows, err := c.Query(ctx, query,
		clickhouse.DateNamed("from", minTs, clickhouse.NanoSeconds),
		clickhouse.DateNamed("to", maxTs, clickhouse.NanoSeconds),
		clickhouse.Named("traceIds", traceIds),
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var eventsTimestamp []time.Time
	var eventsName []string
	var eventsAttributes []map[string]string
	for rows.Next() {
		var s model.TraceSpan
		err = rows.Scan(
			&s.Timestamp, &s.TraceId, &s.SpanId, &s.ParentSpanId, &s.Name, &s.ServiceName, &s.Client, &s.Server, &s.Duration,
			&s.StatusCode, &s.StatusMessage, &s.ResourceAttributes, &s.SpanAttributes,
			&eventsTimestamp, &eventsName, &eventsAttributes)
		if err != nil {
			return err
		}
		if res[s.TraceId] == nil {
			res[s.TraceId] = &model.Trace{}
		}

		if l := len(eventsTimestamp); l > 0 && l == len(eventsName) && l == len(eventsAttributes) {
			s.Events = make([]model.TraceSpanEvent, l)
			for i := range eventsTimestamp {
				s.Events[i].Timestamp = eventsTimestamp[i]
				s.Events[i].Name = eventsName[i]
				s.Events[i].Attributes = eventsAttributes[i]
			}
		}
		res[s.TraceId].Spans = append(res[s.TraceId].Spans, &s)
	}
	return nil
}

func (c *Client) getTraceErrors(ctx context.Context, q SpanQuery) (map[model.TraceSpanKey]*model.TraceErrorsStat, error) {
	filters, filterArgs := q.RootSpansFilter(false)
	q.Errors = true
	durFilter, durFilterArgs := q.SelectionFilter()
	if durFilter != "" {
		filters = append(filters, durFilter)
		filterArgs = append(filterArgs, durFilterArgs...)
	}
	traces, err := c.getTraces(ctx, q.TsFrom, q.TsTo, filters, filterArgs)
	if err != nil {
		return nil, err
	}

	errors := map[model.TraceSpanKey]*model.TraceErrorsStat{}
	var total float32
	for _, t := range traces {
		parents := map[string]string{}
		errorSpans := map[string]*model.TraceSpan{}
		for _, s := range t.Spans {
			parents[s.SpanId] = s.ParentSpanId
			if s.StatusCode == "STATUS_CODE_ERROR" {
				errorSpans[s.SpanId] = s
			}
		}
		for _, s := range errorSpans {
			parentId := s.ParentSpanId
			for parentId != "" {
				delete(errorSpans, parentId)
				parentId = parents[parentId]
			}
		}

		for _, s := range errorSpans {
			ls := s.Labels()
			k := model.TraceSpanKey{
				Cluster:     c.Project().Name,
				ServiceName: s.ServiceName,
				SpanName:    s.Name,
				LabelsHash:  ls.Hash(),
			}
			if errors[k] == nil {
				errors[k] = &model.TraceErrorsStat{
					TraceSpanKey:  k,
					Labels:        ls,
					SampleTraceId: s.TraceId,
					SampleError:   s.ErrorMessage(),
				}
			}
			errors[k].Count++
			total++
		}
	}
	return errors, nil
}

func (c *Client) GetSelectionAndBaselineTraces(ctx context.Context, q SpanQuery) ([]*model.Trace, []*model.Trace, error) {
	filters, filterArgs := q.RootSpansFilter(false)

	var err error
	var selectionFilter string
	var selectionTraces []*model.Trace
	if q.IsSelectionDefined() {
		selectionFilter = "Timestamp BETWEEN @tsFrom AND @tsTo"
		filterArgs = append(filterArgs,
			clickhouse.DateNamed("tsFrom", q.TsFrom.ToStandard(), clickhouse.NanoSeconds),
			clickhouse.DateNamed("tsTo", q.TsTo.ToStandard(), clickhouse.NanoSeconds),
		)
		durFilter, durFilterArgs := q.SelectionFilter()
		if durFilter != "" {
			selectionFilter += " AND " + durFilter
			filterArgs = append(filterArgs, durFilterArgs...)
		}
		selectionTraces, err = c.getTraces(ctx, q.TsFrom, q.TsTo, append(filters, selectionFilter), filterArgs)
		if err != nil {
			return nil, nil, err
		}
		if !q.Diff {
			return selectionTraces, nil, nil
		}
	}

	if selectionFilter != "" {
		filters = append(filters, fmt.Sprintf("NOT (%s)", selectionFilter))
	}
	baselineTraces, err := c.getTraces(ctx, q.Ctx.From, q.Ctx.To, filters, filterArgs)
	if err != nil {
		return nil, nil, err
	}
	return selectionTraces, baselineTraces, nil
}

type SpanFilter struct {
	Field string
	Op    string
	Value string
}

type SpanQuery struct {
	Ctx timeseries.Context

	TsFrom  timeseries.Time
	TsTo    timeseries.Time
	DurFrom time.Duration
	DurTo   time.Duration
	Errors  bool

	Limit int

	Filters          []SpanFilter
	ExcludePeerAddrs []string
	Sort             Sort

	Diff bool

	// WithBodies selects the request and response bodies too.
	WithBodies bool
}

func (q *SpanQuery) AddFilter(field, op, value string) {
	q.Filters = append(q.Filters, SpanFilter{Field: field, Op: op, Value: value})
}

func (q *SpanQuery) IsSelectionDefined() bool {
	return q.TsFrom > q.Ctx.From || q.TsTo < q.Ctx.To || q.DurFrom > 0 || q.DurTo > 0 || q.Errors
}

// SelectionFilter narrows raw spans to the selection made on the charts: a
// duration range, or errors (links made with the former heatmap).
func (q *SpanQuery) SelectionFilter() (string, []any) {
	var filter string
	switch {
	case q.DurFrom > 0 && q.DurTo > 0 && q.Errors:
		filter = "(Duration BETWEEN @durFrom AND @durTo OR StatusCode = 'STATUS_CODE_ERROR')"
	case q.DurFrom == 0 && q.DurTo > 0 && q.Errors:
		filter = "(Duration <= @durTo OR StatusCode = 'STATUS_CODE_ERROR')"
	case q.DurFrom > 0 && q.DurTo == 0 && q.Errors:
		filter = "(Duration >= @durFrom OR StatusCode = 'STATUS_CODE_ERROR')"
	case q.DurFrom == 0 && q.DurTo == 0 && q.Errors:
		filter = "StatusCode = 'STATUS_CODE_ERROR'"
	case q.DurFrom > 0 && q.DurTo > 0 && !q.Errors:
		filter = "Duration BETWEEN @durFrom AND @durTo"
	case q.DurFrom == 0 && q.DurTo > 0 && !q.Errors:
		filter = "Duration <= @durTo"
	case q.DurFrom > 0 && q.DurTo == 0 && !q.Errors:
		filter = "Duration >= @durFrom"
	}
	var args []any
	if q.DurFrom > 0 {
		args = append(args, clickhouse.Named("durFrom", q.DurFrom.Nanoseconds()))
	}
	if q.DurTo > 0 {
		args = append(args, clickhouse.Named("durTo", q.DurTo.Nanoseconds()))
	}
	return filter, args
}

func rootSpanCondition(fromMV bool) string {
	if fromMV {
		return "Root = 1"
	}
	return "ParentSpanId = ''"
}

func (q *SpanQuery) RootSpansFilter(fromMV bool) ([]string, []any) {
	return q.rootSpansFilter(fromMV, "")
}

// rootSpansFilter selects the root spans of the Traces page: one per request,
// the one the collector marked as counted when the two sides recorded it.
func (q *SpanQuery) rootSpansFilter(fromMV bool, skipField string) ([]string, []any) {
	filter, args := q.filter(skipField, fromMV)
	filter = append(filter, rootSpanCondition(fromMV), "Counted = 1")
	if len(q.ExcludePeerAddrs) > 0 {
		filter = append(filter, "NetSockPeerAddr NOT IN (@addrs)")
		args = append(args, clickhouse.Named("addrs", q.ExcludePeerAddrs))
	}
	return filter, args
}

var (
	tracesHistogramMVLock      sync.Mutex
	tracesHistogramMVCreatedAt = map[db.ProjectId]time.Time{}
)

func (c *Client) tracesHistogramCreatedAt(ctx context.Context) time.Time {
	tracesHistogramMVLock.Lock()
	t, ok := tracesHistogramMVCreatedAt[c.project.Id]
	tracesHistogramMVLock.Unlock()
	if ok {
		return t
	}
	err := c.conn.QueryRow(ctx,
		"SELECT metadata_modification_time FROM system.tables WHERE database = currentDatabase() AND name = 'otel_traces_histogram_mv'",
	).Scan(&t)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			klog.Warningln(err)
		}
		return time.Time{}
	}
	tracesHistogramMVLock.Lock()
	tracesHistogramMVCreatedAt[c.project.Id] = t
	tracesHistogramMVLock.Unlock()
	return t
}

func (c *Client) useTracesHistogram(ctx context.Context, q SpanQuery, from timeseries.Time) bool {
	createdAt := c.tracesHistogramCreatedAt(ctx)
	return q.filtersOnHistogramDimensions() && !createdAt.IsZero() && from.ToStandard().After(createdAt)
}

func (q *SpanQuery) filtersOnHistogramDimensions() bool {
	for _, f := range q.Filters {
		switch f.Field {
		// Source is derived from ServiceName, Status is the StatusClass column,
		// Namespace is derived from Server
		case "ServiceName", "SpanName", "SpanKind", "NetSockPeerAddr", "Source", "Status", "Client", "Server", "Namespace":
		default:
			return false
		}
	}
	return true
}

func (q *SpanQuery) SpansByServiceNameFilter(fromMV bool) ([]string, []any) {
	filter, args := q.Filter(fromMV)
	filter = append(filter, fmt.Sprintf("(SpanKind = 'SPAN_KIND_SERVER' OR SpanKind = 'SPAN_KIND_CONSUMER' OR (SpanKind = 'SPAN_KIND_INTERNAL' AND %s))", rootSpanCondition(fromMV)))
	if len(q.ExcludePeerAddrs) > 0 {
		filter = append(filter, "NetSockPeerAddr NOT IN (@addrs)")
		args = append(args, clickhouse.Named("addrs", q.ExcludePeerAddrs))
	}
	return filter, args
}

func (q *SpanQuery) Filter(fromMV bool) ([]string, []any) {
	return q.filter("", fromMV)
}

// filter makes the conditions of the filters, one or more per field, as on
// Logs: the values a field may have (=, ~) are alternatives, the values it must
// not have (!=, !~) all apply, and the fields apply together.
func (q *SpanQuery) filter(skipField string, fromMV bool) ([]string, []any) {
	var fields []string
	alternatives := map[string][]string{}
	exclusions := map[string][]string{}
	var args []any
	for i, f := range q.Filters {
		if skipField != "" && f.Field == skipField {
			continue
		}
		name := fmt.Sprintf("filter_%d", i)
		var expr string
		if f.Field == "Namespace" {
			var ok bool
			if expr, ok = traceNamespacePredicate(f.Op, name, f.Value); !ok {
				continue
			}
		} else if base, ok := traceDerivedFieldExpr(f.Field, fromMV); ok {
			if expr, ok = derivedFieldPredicate(base, f.Op, name); !ok {
				continue
			}
		} else {
			if strings.ContainsFunc(f.Field, func(r rune) bool { return !unicode.IsLetter(r) }) {
				continue
			}
			switch f.Op {
			case "=":
				expr = "%s = @%s"
			case "!=":
				expr = "%s != @%s"
			case "~":
				expr = "match(%s, @%s)"
			case "!~":
				expr = "NOT match(%s, @%s)"
			default:
				continue
			}
			expr = fmt.Sprintf(expr, f.Field, name)
		}
		if _, seen := alternatives[f.Field]; !seen {
			if _, seen = exclusions[f.Field]; !seen {
				fields = append(fields, f.Field)
			}
		}
		if f.Op == "=" || f.Op == "~" {
			alternatives[f.Field] = append(alternatives[f.Field], expr)
		} else {
			exclusions[f.Field] = append(exclusions[f.Field], expr)
		}
		args = append(args, clickhouse.Named(name, f.Value))
	}
	var filter []string
	for _, field := range fields {
		filter = append(filter, exclusions[field]...)
		switch fieldAlternatives := alternatives[field]; len(fieldAlternatives) {
		case 0:
		case 1:
			filter = append(filter, fieldAlternatives[0])
		default:
			filter = append(filter, "("+strings.Join(fieldAlternatives, " OR ")+")")
		}
	}
	return filter, args
}

func inboundSpansFilter(clients []string, listens []model.Listen, fromMV bool) ([]string, []any) {
	ips := map[string]bool{}
	for _, l := range listens {
		if l.Port == "0" {
			ips[l.IP] = true
		}
	}
	var addrs []clickhouse.GroupSet
	for _, l := range listens {
		addrs = append(addrs, clickhouse.GroupSet{Value: []any{l.IP, l.Port}})
	}
	peerName, peerPort := "SpanAttributes['net.peer.name']", "SpanAttributes['net.peer.port']"
	if fromMV {
		peerName, peerPort = "NetPeerName", "NetPeerPort"
	}
	filter := []string{
		"ServiceName IN (@services)",
		fmt.Sprintf("(%s IN (@ips) OR (%s, %s) IN (@addrs))", peerName, peerName, peerPort),
	}
	args := []any{
		clickhouse.Named("services", clients),
		clickhouse.Named("ips", maps.Keys(ips)),
		clickhouse.Named("addrs", addrs),
	}
	return filter, args
}

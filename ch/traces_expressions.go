package ch

// traceHttpStatusCode is the HTTP status code of a span, in the old or the new
// semantic convention, as a number; 0 when the span has none or it is not a number.
const traceHttpStatusCode = "toUInt16OrZero(if(SpanAttributes['http.status_code'] != '', SpanAttributes['http.status_code'], SpanAttributes['http.response.status_code']))"

// TraceStatusClassExpression is the status class of a span: 2xx, 3xx, 4xx or 5xx
// for an HTTP status code from 200 to 599, otherwise error when the span failed
// and other when it did not. The histogram materialized view and the queries on
// raw spans use it both, so that they give the same classes.
const TraceStatusClassExpression = "multiIf(" +
	traceHttpStatusCode + " BETWEEN 200 AND 599, concat(toString(intDiv(" + traceHttpStatusCode + ", 100)), 'xx'), " +
	"StatusCode = 'STATUS_CODE_ERROR', 'error', " +
	"'other')"

// TraceStatusClasses lists the status classes in the order the charts show them.
var TraceStatusClasses = []string{"2xx", "3xx", "4xx", "5xx", "error", "other"}

// traceHttpMethod is the HTTP method of a span, in the old or the new semantic convention.
const traceHttpMethod = "if(SpanAttributes['http.method'] != '', SpanAttributes['http.method'], SpanAttributes['http.request.method'])"

// traceHttpPath is the path a span requested, without the query: the route of
// the framework, or the target, or the path, or the path of the whole URL,
// which is what the node agent (eBPF) records.
const traceHttpPath = "multiIf(" +
	"SpanAttributes['http.route'] != '', substringIndex(SpanAttributes['http.route'], char(63), 1), " +
	"SpanAttributes['http.target'] != '', substringIndex(SpanAttributes['http.target'], char(63), 1), " +
	"SpanAttributes['url.path'] != '', substringIndex(SpanAttributes['url.path'], char(63), 1), " +
	"path(SpanAttributes['http.url']))"

// traceApiRouteExpression is the API route of a span, METHOD /path, the way
// the trace list shows it (traceApiRoute in front/src/utils/traceColumns.js);
// empty for a span that is not an HTTP request.
const traceApiRouteExpression = "if(" + traceHttpPath + " = '', '', " +
	"if(" + traceHttpMethod + " = '', " + traceHttpPath + ", concat(" + traceHttpMethod + ", ' ', " + traceHttpPath + ")))"

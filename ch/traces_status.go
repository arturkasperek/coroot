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

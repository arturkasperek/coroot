'use strict';

const { isSpanContextValid, trace } = require('@opentelemetry/api');

function currentTraceContext() {
  const span = trace.getActiveSpan();
  if (!span) {
    return {};
  }
  const ctx = span.spanContext();
  if (!ctx || !isSpanContextValid(ctx)) {
    return {};
  }
  return { trace_id: ctx.traceId, span_id: ctx.spanId };
}

function emit(severityText, body, attributes, isError) {
  const line = JSON.stringify({ severityText, body, ...currentTraceContext(), ...(attributes || {}) });
  if (isError) {
    console.error(line);
  } else {
    console.log(line);
  }
}

module.exports = {
  info: (body, attributes) => emit('INFO', body, attributes, false),
  error: (body, attributes) => emit('ERROR', body, attributes, true),
};

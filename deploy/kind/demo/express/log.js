'use strict';

const { logs, SeverityNumber } = require('@opentelemetry/api-logs');

function emit(severityNumber, severityText, body, attributes) {
  logs.getLogger(process.env.OTEL_SERVICE_NAME || 'app').emit({
    severityNumber,
    severityText,
    body,
    attributes: attributes || {},
  });
  const line = JSON.stringify({ severityText, body, ...(attributes || {}) });
  if (severityNumber >= SeverityNumber.ERROR) {
    console.error(line);
  } else {
    console.log(line);
  }
}

module.exports = {
  info: (body, attributes) => emit(SeverityNumber.INFO, 'INFO', body, attributes),
  error: (body, attributes) => emit(SeverityNumber.ERROR, 'ERROR', body, attributes),
};

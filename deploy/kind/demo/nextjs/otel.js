'use strict';

if (globalThis.__nextjsDemoOtelStarted) {
  module.exports = {};
} else {
  globalThis.__nextjsDemoOtelStarted = true;

  process.env.OTEL_LOGS_EXPORTER = process.env.OTEL_LOGS_EXPORTER || 'none';

  const { NodeSDK } = require('@opentelemetry/sdk-node');
  const { getNodeAutoInstrumentations } = require('@opentelemetry/auto-instrumentations-node');
  const { OTLPTraceExporter } = require('@opentelemetry/exporter-trace-otlp-proto');
  const { PrometheusExporter } = require('@opentelemetry/exporter-prometheus');
  const { Resource } = require('@opentelemetry/resources');

  const endpoint = (process.env.OTEL_EXPORTER_OTLP_ENDPOINT || 'http://coroot:8080').replace(/\/$/, '');
  const serviceName = process.env.OTEL_SERVICE_NAME || 'nextjs-demo';

  const sdk = new NodeSDK({
    resource: new Resource({
      'service.name': serviceName,
      'deployment.environment': 'dev',
    }),
    traceExporter: new OTLPTraceExporter({ url: `${endpoint}/v1/traces` }),
    metricReader: new PrometheusExporter({ port: Number(process.env.OTEL_EXPORTER_PROMETHEUS_PORT || 9464), host: '0.0.0.0' }),
    instrumentations: [
      getNodeAutoInstrumentations({
        '@opentelemetry/instrumentation-fs': { enabled: false },
        '@opentelemetry/instrumentation-http': {
          requestHook(span, request) {
            if (typeof request.httpVersion !== 'string' || typeof request.url !== 'string') {
              return;
            }
            const path = request.url.split('?')[0] || '/';
            span.updateName(`${request.method} ${path}`);
          },
        },
      }),
    ],
  });

  sdk.start();
}

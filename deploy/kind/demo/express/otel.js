'use strict';

process.env.OTEL_LOGS_EXPORTER = process.env.OTEL_LOGS_EXPORTER || 'none';

const { NodeSDK } = require('@opentelemetry/sdk-node');
const { getNodeAutoInstrumentations } = require('@opentelemetry/auto-instrumentations-node');
const { OTLPTraceExporter } = require('@opentelemetry/exporter-trace-otlp-proto');
const { PrometheusExporter } = require('@opentelemetry/exporter-prometheus');
const { Resource } = require('@opentelemetry/resources');

const endpoint = (process.env.OTEL_EXPORTER_OTLP_ENDPOINT || 'http://coroot:8080').replace(/\/$/, '');
const serviceName = process.env.OTEL_SERVICE_NAME || 'app';

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
    }),
  ],
});

sdk.start();

process.on('SIGTERM', () => {
  sdk.shutdown().finally(() => process.exit(0));
});

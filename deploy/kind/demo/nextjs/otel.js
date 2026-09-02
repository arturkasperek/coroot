'use strict';

const { NodeSDK } = require('@opentelemetry/sdk-node');
const { getNodeAutoInstrumentations } = require('@opentelemetry/auto-instrumentations-node');
const { OTLPTraceExporter } = require('@opentelemetry/exporter-trace-otlp-proto');
const { OTLPLogExporter } = require('@opentelemetry/exporter-logs-otlp-proto');
const { PrometheusExporter } = require('@opentelemetry/exporter-prometheus');
const { BatchLogRecordProcessor } = require('@opentelemetry/sdk-logs');
const { Resource } = require('@opentelemetry/resources');

const endpoint = (process.env.OTEL_EXPORTER_OTLP_ENDPOINT || 'http://coroot:8080').replace(/\/$/, '');
const serviceName = process.env.OTEL_SERVICE_NAME || 'nextjs-demo';

const sdk = new NodeSDK({
  resource: new Resource({
    'service.name': serviceName,
    'deployment.environment': 'dev',
  }),
  traceExporter: new OTLPTraceExporter({ url: `${endpoint}/v1/traces` }),
  logRecordProcessors: [new BatchLogRecordProcessor(new OTLPLogExporter({ url: `${endpoint}/v1/logs` }))],
  metricReader: new PrometheusExporter({ port: Number(process.env.OTEL_EXPORTER_PROMETHEUS_PORT || 9464), host: '0.0.0.0' }),
  instrumentations: [
    getNodeAutoInstrumentations({
      '@opentelemetry/instrumentation-fs': { enabled: false },
    }),
  ],
});

sdk.start();

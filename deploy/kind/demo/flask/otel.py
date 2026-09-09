import atexit
import os

from opentelemetry import metrics, trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.exporter.prometheus import PrometheusMetricReader
from opentelemetry.instrumentation.flask import FlaskInstrumentor
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from prometheus_client import start_http_server


def setup(app):
    os.environ.setdefault("OTEL_LOGS_EXPORTER", "none")
    service_name = os.environ.get("OTEL_SERVICE_NAME", "flask-demo")
    endpoint = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", "http://coroot:8080").rstrip("/")
    prometheus_port = int(os.environ.get("OTEL_EXPORTER_PROMETHEUS_PORT", "9464"))

    resource = Resource.create(
        {
            "service.name": service_name,
            "deployment.environment": "dev",
        }
    )

    provider = TracerProvider(resource=resource)
    provider.add_span_processor(
        BatchSpanProcessor(OTLPSpanExporter(endpoint=f"{endpoint}/v1/traces"))
    )
    trace.set_tracer_provider(provider)

    start_http_server(port=prometheus_port, addr="0.0.0.0")
    metrics.set_meter_provider(
        MeterProvider(resource=resource, metric_readers=[PrometheusMetricReader()])
    )

    FlaskInstrumentor().instrument_app(app)
    atexit.register(provider.shutdown)
    return metrics.get_meter("flask-demo")

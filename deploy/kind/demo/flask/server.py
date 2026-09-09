import json
import os
import sys
import time

from flask import Flask, jsonify, request

app = Flask(__name__)
requests_counter = None
hellos_counter = None


def emit(severity, body, is_error=False, **attrs):
    line = json.dumps({"severityText": severity, "body": body, **attrs})
    print(line, file=sys.stderr if is_error else sys.stdout, flush=True)


@app.after_request
def count_request(resp):
    if requests_counter is not None:
        requests_counter.add(
            1,
            {"method": request.method, "route": request.path, "status": str(resp.status_code)},
        )
    return resp


@app.get("/health")
def health():
    return "ok", 200


@app.get("/api/hello")
def hello():
    if hellos_counter is not None:
        hellos_counter.add(1)
    token = request.args.get("token")
    emit("INFO", f"flask hello {token}" if token else "flask hello", path=request.path)
    return jsonify(service="flask-demo", message="hello from flask", ts=int(time.time() * 1000))


@app.get("/api/slow")
def slow():
    emit("INFO", "flask slow path")
    time.sleep(0.25)
    return jsonify(service="flask-demo", slow=True)


@app.get("/api/error")
def error():
    emit("ERROR", "flask simulated failure", is_error=True)
    return jsonify(service="flask-demo", error="simulated"), 500


def init_observability():
    global requests_counter, hellos_counter
    from otel import setup

    meter = setup(app)
    requests_counter = meter.create_counter(
        "flask_demo_requests_total",
        description="HTTP requests handled by flask-demo",
    )
    hellos_counter = meter.create_counter(
        "flask_demo_hello_total",
        description="GET /api/hello requests handled by flask-demo",
    )


if __name__ == "__main__":
    init_observability()
    port = int(os.environ.get("PORT", "3000"))
    emit("INFO", "flask-demo listening", port=str(port))
    app.run(host="0.0.0.0", port=port, threaded=True)

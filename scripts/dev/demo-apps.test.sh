#!/usr/bin/env bash
# Unit test for make-dev demo app wiring (no cluster required).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
APPS="$ROOT/deploy/kind/demo/apps.yaml"
TILT="$ROOT/Tiltfile"
MAKEFILE="$ROOT/Makefile"
DEV="$ROOT/scripts/dev/dev.sh"
DOCKERFILE="$ROOT/deploy/kind/demo/symfony/Dockerfile"
COMPOSER="$ROOT/deploy/kind/demo/symfony/composer.json"
CONTROLLER="$ROOT/deploy/kind/demo/symfony/src/Controller/ApiController.php"

fail() { echo "$*" >&2; exit 1; }

[[ -f "$APPS" ]] || fail "missing $APPS"

grep -q 'name: symfony-demo$' "$APPS" || fail "apps.yaml missing symfony-demo"
grep -q 'image: symfony-demo:dev' "$APPS" || fail "apps.yaml missing symfony-demo:dev image"
grep -q 'name: symfony-demo-db$' "$APPS" || fail "apps.yaml missing dedicated symfony-demo-db (must not reuse Coroot postgres)"
grep -q 'DATABASE_URL' "$APPS" || fail "symfony-demo missing DATABASE_URL"
grep -q 'symfony-demo-db' "$APPS" || fail "symfony-demo DATABASE_URL must point at symfony-demo-db"

grep -Eq 'http://symfony-demo:[0-9]+/api/hello' "$APPS" || fail "demo-traffic missing http://symfony-demo:<port>/api/hello"
grep -Eq 'http://symfony-demo:[0-9]+/api/visits' "$APPS" || fail "demo-traffic missing http://symfony-demo:<port>/api/visits"

grep -q "'symfony-demo'" "$TILT" || fail "Tiltfile missing symfony-demo image"
grep -q 'deploy/kind/demo/symfony' "$TILT" || fail "Tiltfile missing Symfony build context"
grep -q 'symfony-demo-db' "$TILT" || fail "Tiltfile missing symfony-demo-db resource"

[[ -f "$DOCKERFILE" ]] || fail "missing $DOCKERFILE"
grep -qi 'frankenphp' "$DOCKERFILE" || fail "Dockerfile is not based on FrankenPHP"
grep -qi 'pdo_pgsql' "$DOCKERFILE" || fail "Dockerfile missing pdo_pgsql"

[[ -f "$COMPOSER" ]] || fail "missing $COMPOSER"
grep -q 'symfony/framework-bundle' "$COMPOSER" || fail "composer.json missing Symfony"
grep -q 'doctrine/doctrine-bundle' "$COMPOSER" || fail "composer.json missing Doctrine"

[[ -f "$CONTROLLER" ]] || fail "missing $CONTROLLER"
grep -q 'INSERT' "$CONTROLLER" || grep -q 'insert(' "$CONTROLLER" || fail "ApiController does not write to postgres"
grep -q 'COUNT' "$CONTROLLER" || grep -q 'fetchOne' "$CONTROLLER" || fail "ApiController does not query postgres"

grep -q 'Symfony' "$DEV" || fail "dev.sh banner missing Symfony demo"
grep -q '13002' "$DEV" || fail "dev.sh banner missing Symfony port 13002"
grep -q 'demo-apps.test.sh' "$MAKEFILE" || fail "Makefile does not run demo-apps.test.sh"

FLASK_DIR="$ROOT/deploy/kind/demo/flask"
FLASK_DOCKERFILE="$FLASK_DIR/Dockerfile"
FLASK_SERVER="$FLASK_DIR/server.py"
FLASK_OTEL="$FLASK_DIR/otel.py"
FLASK_REQ="$FLASK_DIR/requirements.txt"

grep -q 'name: flask-demo$' "$APPS" || fail "apps.yaml missing flask-demo"
grep -q 'image: flask-demo:dev' "$APPS" || fail "apps.yaml missing flask-demo:dev image"
grep -q 'OTEL_SERVICE_NAME' "$APPS" || fail "apps.yaml missing OTEL_SERVICE_NAME"
grep -A2 'name: OTEL_SERVICE_NAME' "$APPS" | grep -q 'flask-demo' || fail "flask-demo missing OTEL_SERVICE_NAME=flask-demo"
grep -q 'OTEL_EXPORTER_OTLP_ENDPOINT' "$APPS" || fail "apps.yaml missing OTEL_EXPORTER_OTLP_ENDPOINT"
grep -q 'http://coroot:8080' "$APPS" || fail "flask-demo OTLP endpoint must be http://coroot:8080"
grep -Eq 'http://flask-demo:[0-9]+/api/hello' "$APPS" || fail "demo-traffic missing http://flask-demo:<port>/api/hello"
grep -Eq 'http://flask-demo:[0-9]+/api/slow' "$APPS" || fail "demo-traffic missing http://flask-demo:<port>/api/slow"
grep -Eq 'http://flask-demo:[0-9]+/api/error' "$APPS" || fail "demo-traffic missing http://flask-demo:<port>/api/error"

grep -q "'flask-demo'" "$TILT" || fail "Tiltfile missing flask-demo image"
grep -q 'deploy/kind/demo/flask' "$TILT" || fail "Tiltfile missing Flask build context"
grep -q '13003:3000' "$TILT" || fail "Tiltfile missing flask-demo port-forward 13003:3000"

[[ -f "$FLASK_DOCKERFILE" ]] || fail "missing $FLASK_DOCKERFILE"
grep -qi 'python' "$FLASK_DOCKERFILE" || fail "Dockerfile is not based on Python"

[[ -f "$FLASK_REQ" ]] || fail "missing $FLASK_REQ"
grep -qi 'flask' "$FLASK_REQ" || fail "requirements.txt missing Flask"
grep -q 'opentelemetry-exporter-otlp-proto-http' "$FLASK_REQ" || fail "requirements.txt missing OTLP HTTP exporter"
grep -q 'opentelemetry-instrumentation-flask' "$FLASK_REQ" || fail "requirements.txt missing Flask instrumentation"

[[ -f "$FLASK_SERVER" ]] || fail "missing $FLASK_SERVER"
grep -q '/api/hello' "$FLASK_SERVER" || fail "server.py missing /api/hello"
grep -q '/api/slow' "$FLASK_SERVER" || fail "server.py missing /api/slow"
grep -q '/api/error' "$FLASK_SERVER" || fail "server.py missing /api/error"
grep -q '/health' "$FLASK_SERVER" || fail "server.py missing /health"

[[ -f "$FLASK_OTEL" ]] || fail "missing $FLASK_OTEL"
grep -q 'OTLPSpanExporter' "$FLASK_OTEL" || fail "otel.py missing OTLPSpanExporter"
grep -q '/v1/traces' "$FLASK_OTEL" || fail "otel.py must export traces to /v1/traces"
grep -q 'FlaskInstrumentor' "$FLASK_OTEL" || fail "otel.py missing FlaskInstrumentor"

grep -q 'http://nextjs-demo:3000/chain' "$APPS" || fail "demo-traffic missing http://nextjs-demo:3000/chain"
grep -q 'FLASK_URL' "$APPS" || fail "express-demo missing FLASK_URL"
grep -q '/api/chain' "$ROOT/deploy/kind/demo/express/server.js" || fail "express server.js missing /api/chain"
[[ -f "$ROOT/deploy/kind/demo/nextjs/pages/chain.js" ]] || fail "missing nextjs pages/chain.js"
grep -q '/api/chain' "$ROOT/deploy/kind/demo/nextjs/pages/chain.js" || fail "nextjs chain page missing /api/chain"

ns_count="$(grep -c 'k8s.namespace.name=$(K8S_NAMESPACE)' "$APPS" || true)"
[[ "$ns_count" -eq 3 ]] || fail "apps.yaml must set k8s.namespace.name on express/flask/nextjs (got $ns_count)"
grep -q 'fieldPath: metadata.namespace' "$APPS" || fail "apps.yaml missing Downward API metadata.namespace"

grep -q 'Flask' "$DEV" || fail "dev.sh banner missing Flask demo"
grep -q '13003' "$DEV" || fail "dev.sh banner missing Flask port 13003"

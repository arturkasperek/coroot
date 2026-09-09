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

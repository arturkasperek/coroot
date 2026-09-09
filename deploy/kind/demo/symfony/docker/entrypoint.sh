#!/bin/sh
set -eu

if [ "${1:-}" = 'frankenphp' ]; then
  echo '[symfony-demo] waiting for postgres'
  n=60
  until php bin/console app:init-db; do
    n=$((n - 1))
    if [ "$n" -le 0 ]; then
      echo '[symfony-demo] postgres did not become ready' >&2
      exit 1
    fi
    sleep 2
  done
fi

exec docker-php-entrypoint "$@"

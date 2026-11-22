#!/usr/bin/env bash
# Runs the compiled API with backend/.env loaded (development).
cd "$(dirname "$0")"
set -a; . ./.env; set +a
export APP_ENV="${APP_ENV:-development}"
exec ./bin/server

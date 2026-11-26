#!/usr/bin/env bash
# Runs the browser e2e suites (NFR-M1) end to end against a throw-away database: the same script in CI
# (.github/workflows/ci.yml, job "e2e") and on a developer machine.
#
#   DATABASE_URL=postgresql://…/btn_ops_e2e?sslmode=disable frontend/e2e/run-ci.sh
#
# 1. migrates DATABASE_URL, seeds the catalogue + Super Admin, the dev accounts (`devtools users`, password
#    DevPass1234!) and one month of fixture activity (`devtools volume -months 1 -scale 0.05`: workers, reports,
#    sales, payroll periods — nothing dated today, which the entry scenarios need free)
# 2. starts the API (AUTH_DEV_LOG_OTP=true so the suites read sign-in codes from its log, JOBS_ENABLED=false so
#    nothing is e-mailed, RATE_LIMIT_AUTH_PER_MIN=600 because every simulated device signs in from 127.0.0.1)
# 3. builds the web app into $OUT_DIR/dist (never frontend/dist) and serves it with `vite preview` in front of the API
# 4. runs the suites one after another (a11y, keyboard-entry, offline-attendance, offline-acceptance) and exits 1 if
#    any failed; logs, the a11y report and failure screenshots stay in $OUT_DIR
#
# Settings (env): DATABASE_URL (required; it is migrated and filled with test data — never point it at real data)
#   API_PORT=4920 WEB_PORT=5920 OUT_DIR=<tmp dir> BIN_DIR=<prebuilt server/migrate/seed/devtools, else built here>
#   CHROME=/usr/bin/google-chrome SUITES="a11y keyboard-entry offline-attendance offline-acceptance" SKIP_BUILD=1
set -euo pipefail

: "${DATABASE_URL:?set DATABASE_URL to a scratch database}"
case $DATABASE_URL in *btn_ops_go\?* | *btn_ops_go | */btn_ops\?* | */btn_ops) echo "refusing to fill a main database with e2e data: $DATABASE_URL" >&2; exit 1 ;; esac
HERE=$(cd "$(dirname "$0")" && pwd)
FRONTEND=$(dirname "$HERE")
BACKEND=$(cd "$FRONTEND/../backend" && pwd)
API_PORT=${API_PORT:-4920}
WEB_PORT=${WEB_PORT:-5920}
OUT_DIR=${OUT_DIR:-$(mktemp -d -t btn-e2e.XXXXXX)}
SUITES=${SUITES:-a11y keyboard-entry offline-attendance offline-acceptance}
export CHROME=${CHROME:-/usr/bin/google-chrome}
mkdir -p "$OUT_DIR"/shots/{a11y,keyboard,offline,acceptance}
log() { printf '\n== %s\n' "$*"; }

if [[ -z ${BIN_DIR:-} ]]; then
  BIN_DIR=$OUT_DIR/bin
  log "building API binaries → $BIN_DIR"
  (cd "$BACKEND" && CGO_ENABLED=0 go build -trimpath -tags timetzdata -o "$BIN_DIR/" ./cmd/server ./cmd/migrate ./cmd/seed ./cmd/devtools)
fi

# Environment shared by the API and the tools. Real env vars win over backend/.env, so nothing below can reach the
# developer's own database or mail server.
export APP_ENV=development JOBS_ENABLED=false AUTH_DEV_LOG_OTP=true RATE_LIMIT_AUTH_PER_MIN=600
export PORT=$API_PORT SMTP_HOST=127.0.0.1 SMTP_PORT=1 OTP_PEPPER=${OTP_PEPPER:-e2e-only-pepper-not-a-secret-0123456789abcdef}
export APP_URL=http://localhost:$WEB_PORT FRONTEND_ORIGIN="http://localhost:$WEB_PORT,http://127.0.0.1:$WEB_PORT"
export WEBAUTHN_RP_ID=localhost WEBAUTHN_ORIGINS="http://localhost:$WEB_PORT"
# The seeded Super Admin must not be super@btn.test: `seed` creates it with a forced password change, which
# `devtools users` does not reset, and the suites sign in as super@btn.test with the dev password.
export SUPER_ADMIN_EMAIL=e2e-seed-admin@btn.test SUPER_ADMIN_NAME="E2E Seed Administrator" SUPER_ADMIN_PASSWORD=E2eSeedPass1234!

log "database: migrate, seed, dev accounts, fixtures"
"$BIN_DIR/migrate" up
"$BIN_DIR/seed"
"$BIN_DIR/devtools" users
"$BIN_DIR/devtools" volume -months 1 -scale 0.05 || echo "(fixtures already present)"

pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT

log "API on :$API_PORT (log $OUT_DIR/api.log)"
"$BIN_DIR/server" >"$OUT_DIR/api.log" 2>&1 &
pids+=($!)

cd "$FRONTEND"
if [[ -z ${SKIP_BUILD:-} ]]; then
  log "web build → $OUT_DIR/dist"
  pnpm exec vite build --outDir "$OUT_DIR/dist" --emptyOutDir >"$OUT_DIR/build.log" 2>&1 || { tail -40 "$OUT_DIR/build.log"; exit 1; }
fi
log "vite preview on :$WEB_PORT"
API_TARGET=http://127.0.0.1:$API_PORT pnpm exec vite preview --outDir "$OUT_DIR/dist" --port "$WEB_PORT" --strictPort --host 127.0.0.1 >"$OUT_DIR/preview.log" 2>&1 &
pids+=($!)

wait_for() { # url seconds
  local i
  for ((i = 0; i < $2; i++)); do
    curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
    sleep 1
  done
  echo "timeout waiting for $1" >&2
  return 1
}
wait_for "http://127.0.0.1:$API_PORT/api/health?db=1" 60 || { tail -40 "$OUT_DIR/api.log"; exit 1; }
wait_for "http://127.0.0.1:$WEB_PORT/" 60 || { tail -40 "$OUT_DIR/preview.log"; exit 1; }
wait_for "http://127.0.0.1:$WEB_PORT/api/health" 30

export BASE_URL=http://localhost:$WEB_PORT API_LOG=$OUT_DIR/api.log DEVTOOLS=$BIN_DIR/devtools
failed=()
for s in $SUITES; do
  log "suite: $s"
  case $s in
    a11y) env OUT="$OUT_DIR/a11y-report.json" SHOTS="$OUT_DIR/shots/a11y" node e2e/a11y.mjs || failed+=("$s") ;;
    keyboard-entry) env USER_EMAIL=sup1@btn.test PLANT=BTN-1 SHOTS="$OUT_DIR/shots/keyboard" node e2e/keyboard-entry.mjs || failed+=("$s") ;;
    # another site than keyboard-entry: both need a day without attendance
    offline-attendance) env USER_EMAIL=sup2@btn.test PLANT=BTN-2 SHOTS="$OUT_DIR/shots/offline" node e2e/offline-attendance.mjs || failed+=("$s") ;;
    offline-acceptance) env SHOTS="$OUT_DIR/shots/acceptance" node e2e/offline-acceptance.mjs || failed+=("$s") ;;
    load-size) env DIST="$OUT_DIR/dist" node e2e/load-size.mjs || failed+=("$s") ;;
    *) echo "unknown suite $s" >&2; failed+=("$s") ;;
  esac
done

log "artifacts in $OUT_DIR"
if ((${#failed[@]})); then
  echo "FAILED suites: ${failed[*]}" >&2
  exit 1
fi
echo "all e2e suites passed: $SUITES"

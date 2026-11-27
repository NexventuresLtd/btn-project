#!/bin/sh
# BTN Operations — external uptime probe (SRS NFR-M2). POSIX sh + curl only, so it runs on any small Linux box,
# in the `uptime` docker-compose profile (curl image) or from a systemd timer.
#
#   uptime-check.sh [--env-file FILE] [--loop SECONDS] [--test-alert] [--notify SUBJECT < body]
#
# Run it from OUTSIDE the API host (another VM, the office server, a cheap VPS): the in-process health job dies with
# the API process, this one does not. Every run:
#   1. GETs each URL in UPTIME_URLS (default: $UPTIME_BASE_URL/api/health?db=1 and $UPTIME_BASE_URL/) with a timeout;
#      a check passes on HTTP 2xx, within UPTIME_MAX_LATENCY_MS, and (for /api/health) a body containing "ok":true —
#      /api/health?db=1 also pings PostgreSQL, so "API up, database down" fails too.
#   2. Counts consecutive failed runs in $UPTIME_STATE_DIR. After UPTIME_FAILS_BEFORE_ALERT failures in a row it
#      alerts once, repeats every UPTIME_REPEAT_MINUTES while still down, and sends a "recovered" notice when it is
#      back (with the outage length).
#   3. Alerts go to every channel configured: e-mail (UPTIME_ALERT_EMAIL via curl SMTP, or the local `sendmail`)
#      and/or a webhook (UPTIME_WEBHOOK_URL: JSON POST {"text": ...}, works with Slack/Mattermost/Google Chat/ntfy).
#
# --loop N   keep running, one check every N seconds (container mode).  --test-alert   send a test alert and exit.
# --notify S send S + the body read from stdin through the configured alert channels and exit (used by
#            deploy/restore-drill.sh, so every BTN alert goes through one mechanism); exit 1 if no channel worked.
# Exit codes: 0 all checks passed · 1 usage/config error · 2 at least one check failed (systemd marks the unit failed).
# Settings: deploy/uptime.env.example. Schedule: deploy/btn-ops-uptime.timer (every minute).
set -u

log() { printf '%s [btn-uptime] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { printf '%s [btn-uptime] ERROR: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; exit 1; }

ENV_FILE=""
LOOP=0
TEST_ALERT=0
NOTIFY=""
while [ $# -gt 0 ]; do
  case $1 in
    --env-file) [ $# -ge 2 ] || die "--env-file needs a path"; ENV_FILE=$2; shift 2 ;;
    --loop) [ $# -ge 2 ] || die "--loop needs seconds"; LOOP=$2; shift 2 ;;
    --test-alert) TEST_ALERT=1; shift ;;
    --notify) [ $# -ge 2 ] || die "--notify needs a subject"; NOTIFY=$2; shift 2 ;;
    -h | --help) sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done
if [ -z "$ENV_FILE" ] && [ -r /etc/btn-ops/uptime.env ]; then ENV_FILE=/etc/btn-ops/uptime.env; fi
if [ -n "$ENV_FILE" ]; then
  [ -r "$ENV_FILE" ] || die "env file not readable: $ENV_FILE"
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a
fi

: "${UPTIME_BASE_URL:=}"
: "${UPTIME_URLS:=}"
if [ -z "$UPTIME_URLS" ] && [ -n "$NOTIFY" ]; then UPTIME_URLS="(notify only)"; fi
if [ -z "$UPTIME_URLS" ]; then
  [ -n "$UPTIME_BASE_URL" ] || die "set UPTIME_BASE_URL (e.g. https://operations.btnlimited.com) or UPTIME_URLS"
  base=${UPTIME_BASE_URL%/}
  UPTIME_URLS="$base/api/health?db=1 $base/"
fi
: "${UPTIME_NAME:=BTN Operations}"
: "${UPTIME_TIMEOUT:=10}"
: "${UPTIME_MAX_LATENCY_MS:=5000}"
: "${UPTIME_FAILS_BEFORE_ALERT:=2}"
: "${UPTIME_REPEAT_MINUTES:=60}"
: "${UPTIME_STATE_DIR:=${STATE_DIRECTORY:-/var/lib/btn-ops-uptime}}"
: "${UPTIME_ALERT_EMAIL:=}"
: "${UPTIME_SMTP_URL:=}"
: "${UPTIME_SMTP_USER:=}"
: "${UPTIME_SMTP_PASSWORD:=}"
: "${UPTIME_SMTP_PASSWORD_FILE:=}"
: "${UPTIME_MAIL_FROM:=btn-uptime@localhost}"
: "${UPTIME_WEBHOOK_URL:=}"
: "${UPTIME_CURL_OPTS:=}"

case $UPTIME_FAILS_BEFORE_ALERT in '' | *[!0-9]*) die "UPTIME_FAILS_BEFORE_ALERT must be a number" ;; esac
case $UPTIME_REPEAT_MINUTES in '' | *[!0-9]*) die "UPTIME_REPEAT_MINUTES must be a number" ;; esac
case $LOOP in '' | *[!0-9]*) die "--loop needs a number of seconds" ;; esac
command -v curl >/dev/null 2>&1 || die "curl is required"
mkdir -p "$UPTIME_STATE_DIR" 2>/dev/null || die "cannot create state dir $UPTIME_STATE_DIR"
[ -n "$UPTIME_ALERT_EMAIL" ] || [ -n "$UPTIME_WEBHOOK_URL" ] || log "WARN: no alert channel set (UPTIME_ALERT_EMAIL / UPTIME_WEBHOOK_URL): failures are only logged"

now() { date +%s; }
host_name() { hostname 2>/dev/null || uname -n; }

json_escape() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | awk 'BEGIN{ORS="\\n"} {print}' | sed 's/\\n$//'; }

send_email() { # subject body
  [ -n "$UPTIME_ALERT_EMAIL" ] || return 0
  msg=$(mktemp) || return 1
  {
    printf 'From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n' \
      "$UPTIME_MAIL_FROM" "$UPTIME_ALERT_EMAIL" "$1" "$(date -R 2>/dev/null || date)"
    printf '%s\n' "$2"
  } >"$msg"
  rc=0
  if [ -n "$UPTIME_SMTP_URL" ]; then
    pass=$UPTIME_SMTP_PASSWORD
    pf=$UPTIME_SMTP_PASSWORD_FILE
    # systemd LoadCredential= (deploy/btn-ops-uptime.service)
    if [ -z "$pf" ] && [ -n "${CREDENTIALS_DIRECTORY:-}" ] && [ -r "$CREDENTIALS_DIRECTORY/smtp-pass" ]; then pf=$CREDENTIALS_DIRECTORY/smtp-pass; fi
    if [ -n "$pf" ]; then pass=$(cat "$pf" 2>/dev/null) || pass=""; fi
    cfg=$(mktemp) || return 1
    # credentials go through a curl config file, never the command line (ps)
    if [ -n "$UPTIME_SMTP_USER" ]; then printf 'user = "%s:%s"\n' "$UPTIME_SMTP_USER" "$pass" >"$cfg"; fi
    rcpts=""
    for r in $(printf '%s' "$UPTIME_ALERT_EMAIL" | tr ',' ' '); do rcpts="$rcpts --mail-rcpt $r"; done
    # shellcheck disable=SC2086
    curl -sS --max-time 30 --ssl-reqd -K "$cfg" --url "$UPTIME_SMTP_URL" --mail-from "$UPTIME_MAIL_FROM" $rcpts -T "$msg" || rc=$?
    rm -f "$cfg"
  elif command -v sendmail >/dev/null 2>&1; then
    sendmail -t <"$msg" || rc=$?
  else
    log "WARN: UPTIME_ALERT_EMAIL is set but neither UPTIME_SMTP_URL nor sendmail is available"
    rc=1
  fi
  rm -f "$msg"
  [ $rc -eq 0 ] || log "WARN: e-mail alert failed (exit $rc)"
  return $rc
}

send_webhook() { # text
  [ -n "$UPTIME_WEBHOOK_URL" ] || return 0
  # shellcheck disable=SC2086
  curl -sS --max-time 20 -H 'Content-Type: application/json' -d "{\"text\":\"$(json_escape "$1")\"}" $UPTIME_CURL_OPTS "$UPTIME_WEBHOOK_URL" >/dev/null ||
    { log "WARN: webhook alert failed"; return 1; }
}

alert() { # subject body
  log "ALERT: $1"
  send_email "$1" "$2"
  send_webhook "$1
$2"
}

# check URL → prints "ok <ms>" or "fail <reason>"
check() {
  url=$1
  body=$(mktemp) || { echo "fail mktemp"; return; }
  # shellcheck disable=SC2086
  err=$(mktemp) || { rm -f "$body"; echo "fail mktemp"; return; }
  rc=0
  res=$(curl -sS -o "$body" -w '%{http_code} %{time_total}' --max-time "$UPTIME_TIMEOUT" -H 'Cache-Control: no-cache' \
    -A 'btn-uptime-check/1' $UPTIME_CURL_OPTS "$url" 2>"$err") || rc=$?
  if [ $rc -ne 0 ]; then
    echo "fail unreachable (curl $rc: $(tail -n 1 "$err"))"
    rm -f "$body" "$err"
    return
  fi
  rm -f "$err"
  code=${res%% *}
  secs=${res#* }
  ms=$(awk -v s="$secs" 'BEGIN{printf "%d", s*1000}')
  okbody=1
  case $url in
    */api/health* | */api/v1/health*) grep -q '"ok":true' "$body" || okbody=0 ;;
  esac
  rm -f "$body"
  case $code in
    2??) ;;
    *) echo "fail HTTP $code (${ms} ms)"; return ;;
  esac
  [ $okbody -eq 1 ] || { echo "fail health body without \"ok\":true (${ms} ms)"; return; }
  [ "$ms" -le "$UPTIME_MAX_LATENCY_MS" ] || { echo "fail slow: ${ms} ms > ${UPTIME_MAX_LATENCY_MS} ms"; return; }
  echo "ok $ms"
}

read_state() { [ -r "$UPTIME_STATE_DIR/$1" ] && cat "$UPTIME_STATE_DIR/$1" || echo "${2:-0}"; }
write_state() { printf '%s\n' "$2" >"$UPTIME_STATE_DIR/$1"; }

run_once() {
  failed=""
  summary=""
  for url in $UPTIME_URLS; do
    r=$(check "$url")
    summary="$summary
  $url: $r"
    case $r in ok*) ;; *) failed="$failed
  $url: ${r#fail }" ;; esac
  done
  fails=$(read_state fails 0)
  alerted=$(read_state alerted_at 0)
  down_since=$(read_state down_since 0)
  t=$(now)
  if [ -z "$failed" ]; then
    log "UP$summary"
    if [ "$alerted" -gt 0 ]; then
      mins=$(((t - down_since) / 60))
      alert "[RECOVERED] $UPTIME_NAME is back up" "All checks pass again after about $mins minute(s) of failures (probe on $(host_name)).
$summary"
    fi
    write_state fails 0
    write_state alerted_at 0
    write_state down_since 0
    return 0
  fi
  fails=$((fails + 1))
  [ "$down_since" -gt 0 ] || down_since=$t
  write_state fails "$fails"
  write_state down_since "$down_since"
  log "DOWN ($fails in a row)$failed"
  if [ "$fails" -ge "$UPTIME_FAILS_BEFORE_ALERT" ]; then
    if [ "$alerted" -eq 0 ] || [ $((t - alerted)) -ge $((UPTIME_REPEAT_MINUTES * 60)) ]; then
      what="DOWN"
      [ "$alerted" -eq 0 ] || what="STILL DOWN"
      alert "[$what] $UPTIME_NAME is not answering" "$fails consecutive failed checks from $(host_name) since $(date -u -d "@$down_since" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "$down_since").
Failed:$failed

All checks:$summary

Next steps: btnops status · btnops logs (on the API server) · docs: README → Production deployment → Uptime monitoring."
      write_state alerted_at "$t"
    fi
  fi
  return 2
}

if [ -n "$NOTIFY" ]; then
  [ -n "$UPTIME_ALERT_EMAIL" ] || [ -n "$UPTIME_WEBHOOK_URL" ] || die "--notify: no alert channel configured"
  body=$(cat)
  ok=0
  log "NOTIFY: $NOTIFY"
  if [ -n "$UPTIME_ALERT_EMAIL" ] && send_email "$NOTIFY" "$body"; then ok=1; fi
  if [ -n "$UPTIME_WEBHOOK_URL" ] && send_webhook "$NOTIFY
$body"; then ok=1; fi
  [ $ok -eq 1 ] || exit 1
  exit 0
fi
if [ $TEST_ALERT -eq 1 ]; then
  alert "[TEST] $UPTIME_NAME uptime alert" "Test alert from the external uptime probe on $(host_name). URLs checked: $UPTIME_URLS"
  exit $?
fi
if [ "$LOOP" -gt 0 ]; then
  log "checking every ${LOOP}s: $UPTIME_URLS"
  while :; do
    run_once || :
    sleep "$LOOP"
  done
fi
run_once

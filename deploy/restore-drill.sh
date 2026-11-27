#!/usr/bin/env bash
# BTN Operations — scheduled quarterly restore drill (SRS NFR-R1). Run by deploy/btn-ops-restore-drill.timer.
#
#   restore-drill.sh [--env-file FILE]... [--remote] [--no-alert] [--alert-env FILE]
#     --env-file FILE  backup/restore settings (default /etc/btn-ops/backup.env if it exists); repeatable
#     --remote         drill the newest OFF-SITE copy (BACKUP_RCLONE_REMOTE) instead of the local one
#     --alert-env FILE alert channel settings (default /etc/btn-ops/uptime.env): the same UPTIME_ALERT_EMAIL /
#                      UPTIME_SMTP_* / UPTIME_WEBHOOK_URL as deploy/uptime-check.sh, which sends the message
#     --no-alert       only log and write the report (manual runs)
#
# Steps:
#   1. deploy/restore-test.sh --keep --db ${RESTORE_DRILL_DB_PREFIX:-btn_restore_drill}_<stamp> [--remote] [--compare]: newest backup in the
#      deploy/backup.sh format (btn_ops-YYYYmmdd-HHMM[SS].dump.gpg|.age + .sha256) → checksum → decrypt →
#      pg_restore into the throw-away database → migration version + row counts of the key tables (report under
#      $BACKUP_DIR/restore-reports, RPO/RTO evidence). --compare is added when DATABASE_URL (read-only) is set.
#   2. extra checks on the restored database:
#        - migration version ≥ RESTORE_DRILL_MIN_MIGRATION (default: the newest migration shipped in
#          backend/migrations of this checkout) and, with --compare, equal to the live version;
#        - key tables not empty (User, Plant, Product, Material, AuditLog);
#        - balance check: stock and material balances per site/item and the approved petty-cash balance per float,
#          summed from the ledgers, are computable and — when DATABASE_URL is set — equal to the live database's
#          balances over the rows that existed when the backup was taken (createdAt ≤ newest createdAt restored);
#          without live access, no petty-cash float may be negative.
#   3. drops the throw-away database (always, also on failure) and alerts: FAIL always, PASS too unless
#      RESTORE_DRILL_NOTIFY_SUCCESS=0 (the quarterly evidence mail).
# Exit codes: 0 PASS · 1 FAIL (restore, checks or clean-up) · 2 usage.
set -euo pipefail
umask 077

HERE=$(cd "$(dirname "$0")" && pwd)
log()  { printf '%s [restore-drill] %s\n' "$(date -Is)" "$*"; }
warn() { printf '%s [restore-drill] WARN: %s\n' "$(date -Is)" "$*" >&2; }
usage() { sed -n '2,29p' "$0" | sed 's/^# \{0,1\}//'; }

ENV_FILES=() REMOTE=0 ALERT=1 ALERT_ENV=/etc/btn-ops/uptime.env
while (($#)); do
  case $1 in
    --env-file) ENV_FILES+=("${2:?--env-file needs a path}"); shift 2 ;;
    --remote) REMOTE=1; shift ;;
    --no-alert) ALERT=0; shift ;;
    --alert-env) ALERT_ENV=${2:?--alert-env needs a path}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1 (see --help)" >&2; exit 2 ;;
  esac
done
if ((${#ENV_FILES[@]} == 0)) && [[ -f /etc/btn-ops/backup.env ]]; then ENV_FILES=(/etc/btn-ops/backup.env); fi
for f in "${ENV_FILES[@]}"; do
  [[ -r $f ]] || { echo "env file not readable: $f" >&2; exit 2; }
  set -a
  # shellcheck disable=SC1090
  . "$f"
  set +a
done

BTZ="${BACKUP_TZ:-Africa/Kigali}"
STAMP=$(TZ=$BTZ date +%Y%m%d_%H%M%S)
SCRATCH="${RESTORE_DRILL_DB_PREFIX:-btn_restore_drill}_$STAMP"
PSQL=psql
[[ -n ${PG_BIN:-} ]] && PSQL="$PG_BIN/psql"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/btn-drill.XXXXXX")
OUT="$WORK/drill.log"
RESULT=FAIL REASON="" NOTES=()

# ---- connection helpers (same as restore-test.sh: the password never goes into argv) ----------
urldecode() { local s=${1//\\/\\\\}; printf '%b' "${s//%/\\x}"; }
split_url() {
  if [[ $1 =~ ^(postgres(ql)?://)([^:@/?#]*):([^@/?#]*)@(.*)$ ]]; then
    printf '%s\t%s' "${BASH_REMATCH[1]}${BASH_REMATCH[3]}@${BASH_REMATCH[5]}" "$(urldecode "${BASH_REMATCH[4]}")"
  else printf '%s\t' "$1"; fi
}
with_db() {
  if [[ $1 =~ ^(postgres(ql)?://[^/?#]*)(/[^?#]*)?(\?.*)?$ ]]; then
    printf '%s/%s%s' "${BASH_REMATCH[1]}" "$2" "${BASH_REMATCH[4]}"
  else printf '%s dbname=%s' "$1" "$2"; fi
}
ADMIN_URL="${RESTORE_DATABASE_URL:-${DATABASE_URL:-}}"
[[ -n $ADMIN_URL ]] || { echo "set RESTORE_DATABASE_URL or DATABASE_URL" >&2; exit 2; }
IFS=$'\t' read -r ADMIN_CONN ADMIN_PW < <(split_url "$ADMIN_URL"; echo)
ADMIN_PW=${ADMIN_PW:-${PGPASSWORD:-}}
TARGET_CONN=$(with_db "$ADMIN_CONN" "$SCRATCH")
SRC_CONN="" SRC_PW=""
if [[ -n ${DATABASE_URL:-} ]]; then
  IFS=$'\t' read -r SRC_CONN SRC_PW < <(split_url "$DATABASE_URL"; echo)
  SRC_PW=${SRC_PW:-${PGPASSWORD:-}}
fi
admin_psql()  { PGPASSWORD=$ADMIN_PW "$PSQL" -X -q -v ON_ERROR_STOP=1 -At --dbname="$ADMIN_CONN" "$@"; }
target_psql() { PGPASSWORD=$ADMIN_PW "$PSQL" -X -q -v ON_ERROR_STOP=1 -At --dbname="$TARGET_CONN" "$@"; }
source_psql() { PGPASSWORD=$SRC_PW PGOPTIONS="-c default_transaction_read_only=on" \
                  "$PSQL" -X -q -v ON_ERROR_STOP=1 -At --dbname="$SRC_CONN" "$@"; }

notify() { # subject (body on stdin)
  ((ALERT)) || { cat >/dev/null; return 0; }
  if [[ ! -r $ALERT_ENV ]]; then warn "alert settings $ALERT_ENV not readable: no alert sent"; cat >/dev/null; return 1; fi
  UPTIME_STATE_DIR="$WORK/alert-state" sh "$HERE/uptime-check.sh" --env-file "$ALERT_ENV" --notify "$1" || { warn "alert could not be sent"; return 1; }
}

finish() {
  local rc=$?
  set +e
  if [[ -n $(admin_psql -c "SELECT 1 FROM pg_database WHERE datname = '$SCRATCH'" 2>/dev/null) ]]; then
    if admin_psql -c "DROP DATABASE IF EXISTS \"$SCRATCH\" WITH (FORCE)"; then log "dropped $SCRATCH"
    else NOTES+=("could not drop $SCRATCH — drop it by hand"); RESULT=FAIL; rc=1; fi
  fi
  ((rc == 0)) || RESULT=FAIL
  [[ $RESULT == PASS || -n $REASON ]] || REASON="unexpected error (exit $rc), see the log below"
  [[ $RESULT == PASS ]] || rc=1
  log "result: $RESULT${REASON:+ — $REASON}"
  if [[ $RESULT == FAIL || ${RESTORE_DRILL_NOTIFY_SUCCESS:-1} != 0 ]]; then
    {
      echo "Quarterly restore drill on $(hostname): $RESULT${REASON:+ — $REASON}"
      for n in "${NOTES[@]}"; do echo "- $n"; done
      echo
      echo "Log (last 60 lines):"
      tail -n 60 "$OUT" 2>/dev/null
    } | notify "[$RESULT] BTN Operations restore drill $STAMP"
  fi
  rm -rf -- "$WORK"
  exit "$rc"
}
trap finish EXIT
fail() { REASON="$*"; exit 1; }

# ---- 1. restore + standard checks ------------------------------------------------------------
args=(--keep --db "$SCRATCH")
((REMOTE)) && args+=(--remote)
[[ -n $SRC_CONN ]] && args+=(--compare)
for f in "${ENV_FILES[@]}"; do args+=(--env-file "$f"); done
log "restore-test.sh ${args[*]}"
set +e
bash "$HERE/restore-test.sh" "${args[@]}" 2>&1 | tee "$OUT"
rt=${PIPESTATUS[0]}
set -e
((rt == 0)) || fail "restore-test.sh failed (exit $rt): $(grep -m1 'ERROR:' "$OUT" | sed 's/.*ERROR: //')"

# ---- 2a. migration version ---------------------------------------------------------------------
min=${RESTORE_DRILL_MIN_MIGRATION:-}
if [[ -z $min && -d $HERE/../backend/migrations ]]; then
  min=$(find "$HERE/../backend/migrations" -maxdepth 1 -name '[0-9]*_*.sql' -printf '%f\n' | sed 's/_.*//' | sort -n | tail -n 1 | sed 's/^0*//')
fi
ver=$(target_psql -c "SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied") || fail "cannot read goose_db_version"
NOTES+=("restored migration version $ver${min:+ (expected ≥ $min)}")
[[ -z $min || $ver -ge $min ]] || fail "restored migration version $ver is older than the release ($min): the backup predates the last deployment or is from another system"
if [[ -n $SRC_CONN ]]; then
  live=$(source_psql -c "SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied") || fail "cannot read the live migration version"
  [[ $live == "$ver" ]] || NOTES+=("live migration version $live differs from the backup's $ver (a deployment since the backup?)")
fi

# ---- 2b. key tables not empty -------------------------------------------------------------------
for t in User Plant Product Material AuditLog; do
  n=$(target_psql -c "SELECT count(*) FROM public.\"$t\"") || fail "cannot count \"$t\""
  [[ $n -gt 0 ]] || fail "table \"$t\" is empty in the restored database"
done

# ---- 2c. balance check --------------------------------------------------------------------------
# Ledger balances over the rows that existed at backup time (cutoff = newest createdAt in the restore).
# 10 minutes of margin: a transaction that began before the dump but committed after it is not in the backup.
cutoff=$(target_psql -c "SELECT (COALESCE(max(\"createdAt\"), 'epoch') - interval '10 minutes')::text FROM \"StockMovement\"") \
  || fail "cannot read StockMovement"
balances_sql="
SELECT 'stock', md5(COALESCE(string_agg(k || '=' || v, ',' ORDER BY k), '')) FROM (
  SELECT \"plantId\" || '/' || \"productId\" AS k, SUM(quantity)::text AS v FROM \"StockMovement\"
  WHERE \"createdAt\" <= '$cutoff' GROUP BY 1) s
UNION ALL
SELECT 'material', md5(COALESCE(string_agg(k || '=' || v, ',' ORDER BY k), '')) FROM (
  SELECT \"plantId\" || '/' || \"materialId\" AS k, SUM(quantity)::text AS v FROM \"MaterialMovement\"
  WHERE \"createdAt\" <= '$cutoff' GROUP BY 1) m
UNION ALL
SELECT 'pettycash', md5(COALESCE(string_agg(k || '=' || v, ',' ORDER BY k), '')) FROM (
  SELECT l.float_id AS k, SUM(l.effect)::text AS v FROM petty_cash_ledger l
  WHERE l.status = 'APPROVED' AND l.created_at <= '$cutoff' GROUP BY 1) p"
restored_bal=$(target_psql -F '|' -c "$balances_sql") || fail "balance query failed on the restored database"
NOTES+=("ledger balances computed (cut-off $cutoff)")
if [[ -n $SRC_CONN ]]; then
  live_bal=$(source_psql -F '|' -c "$balances_sql") || fail "balance query failed on the live database"
  if [[ $restored_bal != "$live_bal" ]]; then
    diffs=$(diff <(echo "$restored_bal") <(echo "$live_bal") | grep '^[<>]' | cut -c3- | cut -d'|' -f1 | sort -u | tr '\n' ' ' || true)
    fail "balances differ from the live database for rows up to the backup time: $diffs"
  fi
  NOTES+=("stock, material and petty-cash balances equal the live database up to the backup time")
else
  neg=$(target_psql -c "SELECT count(*) FROM (SELECT float_id FROM petty_cash_ledger WHERE status = 'APPROVED' GROUP BY 1 HAVING SUM(effect) < 0) x") \
    || fail "petty-cash balance query failed"
  [[ $neg -eq 0 ]] || fail "$neg petty-cash float(s) have a negative approved balance in the restored database"
  NOTES+=("no live database to compare with: petty-cash floats non-negative")
fi

RESULT=PASS
log "restore drill passed"

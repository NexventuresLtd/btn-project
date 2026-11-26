#!/usr/bin/env bash
# BTN Operations — restore drill (SRS NFR-R1: quarterly restore test, RPO/RTO evidence).
#
#   restore-test.sh [options]
#     --env-file FILE   load settings (default /etc/btn-ops/backup.env if it exists); repeatable
#     --file PATH       test this backup file instead of the newest one
#     --remote          fetch the newest backup from BACKUP_RCLONE_REMOTE instead of BACKUP_DIR
#     --db NAME         scratch database name (default btn_restore_test_<YYYYmmdd_HHMMSS>)
#     --jobs N          parallel pg_restore jobs (default 2)
#     --compare         also count the same tables in the live DB (read-only) and report differences
#     --keep            do not drop the scratch database at the end (drop it yourself afterwards)
#     --reports DIR     where to write the report (default $BACKUP_DIR/restore-reports)
#
# Steps: locate newest backup (daily/weekly/monthly) → [download] → verify .sha256 → decrypt to a
# private temp dir → CREATE DATABASE <scratch> → pg_restore --no-owner --no-acl -j N → row counts of
# key tables + goose_db_version → report (text + JSON) with timings (RTO evidence) and backup age (RPO
# evidence) → DROP DATABASE <scratch>. The source database is only ever read (counts, with
# default_transaction_read_only=on) and only with --compare.
#
# Settings: DATABASE_URL (source, for --compare), RESTORE_DATABASE_URL (server + maintenance DB where
# the scratch DB is created; default DATABASE_URL — use a superuser/createdb role), BACKUP_DIR,
# BACKUP_PASSPHRASE_FILE / BACKUP_PASSPHRASE (gpg) or BACKUP_AGE_IDENTITY (age), BACKUP_RCLONE_REMOTE,
# BACKUP_RCLONE_FLAGS, PG_BIN, RESTORE_REPORT_DIR, BACKUP_TZ. Exit code 0 = PASS, non-zero = FAIL.
set -euo pipefail
umask 077

log()  { printf '%s [restore-test] %s\n' "$(date -Is)" "$*"; }
warn() { printf '%s [restore-test] WARN: %s\n' "$(date -Is)" "$*" >&2; }
ERROR=""
die()  { ERROR="$*"; printf '%s [restore-test] ERROR: %s\n' "$(date -Is)" "$*" >&2; exit 1; }
usage() { sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; }
ms() {  # monotonic milliseconds (immune to wall-clock steps), for the RTO timings
  if [[ -r /proc/uptime ]]; then awk '{ printf "%d", $1 * 1000 }' /proc/uptime; else date +%s%3N; fi
}

load_env_file() {
  [[ -r $1 ]] || die "env file not readable: $1"
  set -a
  # shellcheck disable=SC1090
  . "$1"
  set +a
}

ENV_FILES=() FILE="" USE_REMOTE=0 SCRATCH="" JOBS=2 COMPARE=0 KEEP=0 REPORT_DIR=""
while (($#)); do
  case $1 in
    --env-file) ENV_FILES+=("${2:?--env-file needs a path}"); shift 2 ;;
    --file) FILE=${2:?--file needs a path}; shift 2 ;;
    --remote) USE_REMOTE=1; shift ;;
    --db) SCRATCH=${2:?--db needs a name}; shift 2 ;;
    --jobs) JOBS=${2:?--jobs needs a number}; shift 2 ;;
    --compare) COMPARE=1; shift ;;
    --keep) KEEP=1; shift ;;
    --reports) REPORT_DIR=${2:?--reports needs a directory}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1 (see --help)" >&2; exit 2 ;;
  esac
done
if ((${#ENV_FILES[@]} == 0)) && [[ -f /etc/btn-ops/backup.env ]]; then ENV_FILES=(/etc/btn-ops/backup.env); fi
for f in "${ENV_FILES[@]}"; do load_env_file "$f"; done

DEST="${BACKUP_DIR:-/var/backups/btn-ops}"
BTZ="${BACKUP_TZ:-Africa/Kigali}"
REPORT_DIR="${REPORT_DIR:-${RESTORE_REPORT_DIR:-$DEST/restore-reports}}"
RUN_STAMP=$(TZ=$BTZ date +%Y%m%d_%H%M%S)
SCRATCH="${SCRATCH:-btn_restore_test_$RUN_STAMP}"
[[ $JOBS =~ ^[1-9][0-9]?$ ]] || die "--jobs must be 1..99"
[[ $SCRATCH =~ ^[a-z_][a-z0-9_]{0,62}$ ]] || die "scratch DB name must match ^[a-z_][a-z0-9_]{0,62}\$"

PSQL=psql PG_RESTORE=pg_restore
if [[ -n ${PG_BIN:-} ]]; then PSQL="$PG_BIN/psql" PG_RESTORE="$PG_BIN/pg_restore"; fi
command -v "$PSQL" >/dev/null || die "psql not found (set PG_BIN)"
command -v "$PG_RESTORE" >/dev/null || die "pg_restore not found (set PG_BIN)"

# Connection strings without the password (it goes to PGPASSWORD per command, never into argv).
urldecode() { local s=${1//\\/\\\\}; printf '%b' "${s//%/\\x}"; }
split_url() {  # $1 url -> "conn<TAB>password"
  if [[ $1 =~ ^(postgres(ql)?://)([^:@/?#]*):([^@/?#]*)@(.*)$ ]]; then
    printf '%s\t%s' "${BASH_REMATCH[1]}${BASH_REMATCH[3]}@${BASH_REMATCH[5]}" "$(urldecode "${BASH_REMATCH[4]}")"
  else printf '%s\t' "$1"; fi
}
with_db() {  # $1 conn, $2 dbname -> conn pointing at dbname
  if [[ $1 =~ ^(postgres(ql)?://[^/?#]*)(/[^?#]*)?(\?.*)?$ ]]; then
    printf '%s/%s%s' "${BASH_REMATCH[1]}" "$2" "${BASH_REMATCH[4]}"
  else printf '%s dbname=%s' "$1" "$2"; fi
}
dbname_of() {
  if [[ $1 =~ ^postgres(ql)?://[^/?#]*/([^?#]+) ]]; then urldecode "${BASH_REMATCH[2]}"
  elif [[ $1 =~ (^|[[:space:]])dbname=([^[:space:]]+) ]]; then printf '%s' "${BASH_REMATCH[2]}"; fi
}
ADMIN_URL="${RESTORE_DATABASE_URL:-${DATABASE_URL:-}}"
[[ -n $ADMIN_URL ]] || die "set RESTORE_DATABASE_URL or DATABASE_URL"
IFS=$'\t' read -r ADMIN_CONN ADMIN_PW < <(split_url "$ADMIN_URL"; echo)
ADMIN_PW=${ADMIN_PW:-${PGPASSWORD:-}}
TARGET_CONN=$(with_db "$ADMIN_CONN" "$SCRATCH")
SRC_CONN="" SRC_PW="" SRC_DB=""
if [[ -n ${DATABASE_URL:-} ]]; then
  IFS=$'\t' read -r SRC_CONN SRC_PW < <(split_url "$DATABASE_URL"; echo)
  SRC_PW=${SRC_PW:-${PGPASSWORD:-}}
  SRC_DB=$(dbname_of "$SRC_CONN")
fi
[[ $SCRATCH != "$SRC_DB" && $SCRATCH != "$(dbname_of "$ADMIN_CONN")" ]] || die "scratch DB must differ from the source DB"
((COMPARE == 0)) || [[ -n $SRC_CONN ]] || die "--compare needs DATABASE_URL"

admin_psql()  { PGPASSWORD=$ADMIN_PW "$PSQL" -X -q -v ON_ERROR_STOP=1 -At --dbname="$ADMIN_CONN" "$@"; }
target_psql() { PGPASSWORD=$ADMIN_PW "$PSQL" -X -q -v ON_ERROR_STOP=1 -At --dbname="$TARGET_CONN" "$@"; }
source_psql() { PGPASSWORD=$SRC_PW PGOPTIONS="-c default_transaction_read_only=on" \
                  "$PSQL" -X -q -v ON_ERROR_STOP=1 -At --dbname="$SRC_CONN" "$@"; }

# ---- state for the report + cleanup ---------------------------------------------------------
WORK=$(mktemp -d "${TMPDIR:-/tmp}/btn-restore.XXXXXX")
mkdir -m 700 "$WORK/gnupg"
CREATED=0 DROPPED="no" BACKUP_NAME="" BACKUP_SIZE=0 BACKUP_AGE_H="" SOURCE="local"
T_DOWNLOAD=0 T_VERIFY=0 T_DECRYPT=0 T_RESTORE=0 T_CHECKS=0 T_START=$(ms)
TABLES=(User Plant Product Material Customer StockMovement MaterialMovement Sale Payment PettyCashEntry
        PayrollPeriod Worker Attendance AuditLog)
declare -A RESTORED=() LIVE=()
GOOSE="" LIVE_GOOSE="" CHECK_NOTES=()

secs() { awk -v m="$1" 'BEGIN { printf "%.1f", m / 1000 }'; }
json_str() { local s=${1//\\/\\\\}; s=${s//\"/\\\"}; printf '"%s"' "${s//$'\n'/ }"; }

write_report() {
  local result=$1 total txt json t first=1
  total=$(($(ms) - T_START))
  mkdir -p "$REPORT_DIR"
  txt="$REPORT_DIR/restore-test-$RUN_STAMP.txt"
  json="$REPORT_DIR/restore-test-$RUN_STAMP.json"
  {
    echo "BTN Operations restore test — $(TZ=$BTZ date '+%Y-%m-%d %H:%M %Z')  host=$(hostname)"
    echo "result:          $result${ERROR:+ ($ERROR)}"
    echo "backup:          ${BACKUP_NAME:-?} ($SOURCE, $BACKUP_SIZE bytes)"
    echo "backup age:      ${BACKUP_AGE_H:-?} h   (RPO evidence: data loss window if restored now)"
    echo "scratch db:      $SCRATCH (dropped: $DROPPED)"
    echo "timings (s):     download $(secs $T_DOWNLOAD) · verify $(secs $T_VERIFY) · decrypt $(secs $T_DECRYPT) · restore $(secs $T_RESTORE) · checks $(secs $T_CHECKS) · total $(secs $total)   (RTO evidence)"
    echo "goose version:   restored ${GOOSE:-?}${LIVE_GOOSE:+ / live $LIVE_GOOSE}"
    printf '%-18s %12s %12s %10s\n' table restored live diff
    for t in "${TABLES[@]}"; do
      local r=${RESTORED[$t]:-?} l=${LIVE[$t]:-} d=""
      [[ $r =~ ^[0-9]+$ && $l =~ ^[0-9]+$ ]] && d=$((l - r))
      printf '%-18s %12s %12s %10s\n' "$t" "$r" "${l:--}" "${d:--}"
    done
    for n in "${CHECK_NOTES[@]}"; do echo "note: $n"; done
  } >"$txt"
  {
    printf '{"result":%s,"error":%s,"host":%s,"startedAt":%s,"backup":{"name":%s,"source":%s,"sizeBytes":%s,"ageHours":%s},' \
      "$(json_str "$result")" "$(json_str "$ERROR")" "$(json_str "$(hostname)")" "$(json_str "$RUN_STAMP")" \
      "$(json_str "$BACKUP_NAME")" "$(json_str "$SOURCE")" "$BACKUP_SIZE" "${BACKUP_AGE_H:-null}"
    printf '"scratchDb":%s,"dropped":%s,"timingsSeconds":{"download":%s,"verify":%s,"decrypt":%s,"restore":%s,"checks":%s,"total":%s},' \
      "$(json_str "$SCRATCH")" "$(json_str "$DROPPED")" "$(secs $T_DOWNLOAD)" "$(secs $T_VERIFY)" "$(secs $T_DECRYPT)" \
      "$(secs $T_RESTORE)" "$(secs $T_CHECKS)" "$(secs $total)"
    printf '"gooseVersion":{"restored":%s,"live":%s},"tables":{' "${GOOSE:-null}" "${LIVE_GOOSE:-null}"
    for t in "${TABLES[@]}"; do
      ((first)) || printf ','
      first=0
      printf '%s:{"restored":%s,"live":%s}' "$(json_str "$t")" "${RESTORED[$t]:-null}" "${LIVE[$t]:-null}"
    done
    printf '}}\n'
  } >"$json"
  log "report: $txt"
  log "report: $json"
  cat "$txt"
}

finish() {
  local rc=$?
  set +e
  if ((CREATED)); then
    if ((KEEP)); then
      DROPPED="no (--keep)"
      warn "scratch database $SCRATCH kept; drop it with: DROP DATABASE \"$SCRATCH\";"
    elif admin_psql -c "DROP DATABASE IF EXISTS \"$SCRATCH\" WITH (FORCE)"; then
      DROPPED="yes"; log "dropped scratch database $SCRATCH"
    else
      DROPPED="FAILED"; warn "could not drop scratch database $SCRATCH — drop it by hand"; ((rc)) || rc=1
    fi
  fi
  if ((rc)); then write_report FAIL; else write_report PASS; fi
  gpgconf --homedir "$WORK/gnupg" --kill gpg-agent >/dev/null 2>&1
  rm -rf -- "$WORK"
  exit "$rc"
}
trap finish EXIT

# ---- 1. locate (and download) ---------------------------------------------------------------
NAME_RE='^btn_ops-[0-9]{8}-[0-9]{4,6}\.dump(\.gpg|\.age)?$'
newest() { grep -E "$NAME_RE" | sort -u | tail -n 1; }
if [[ -n $FILE ]]; then
  [[ -f $FILE ]] || die "no such backup file: $FILE"
elif ((USE_REMOTE)); then
  REMOTE="${BACKUP_RCLONE_REMOTE:?--remote needs BACKUP_RCLONE_REMOTE}"
  command -v rclone >/dev/null || die "rclone not installed"
  read -r -a RFLAGS <<<"${BACKUP_RCLONE_FLAGS:-}"
  rjoin() { case $1 in *: | */) printf '%s%s' "$1" "$2" ;; *) printf '%s/%s' "$1" "$2" ;; esac; }
  SOURCE="remote"
  best="" best_tier=""
  for tier in daily weekly monthly; do
    n=$(rclone lsf "${RFLAGS[@]}" --files-only "$(rjoin "$REMOTE" "$tier")/" 2>/dev/null | newest || true)
    if [[ -n $n && $n > $best ]]; then best=$n best_tier=$tier; fi
  done
  [[ -n $best ]] || die "no backups found on $REMOTE"
  t=$(ms)
  mkdir -p "$WORK/dl"
  src=$(rjoin "$REMOTE" "$best_tier")
  rclone copy "${RFLAGS[@]}" "$src/$best" "$WORK/dl" || die "download of $src/$best failed"
  rclone copy "${RFLAGS[@]}" "$src/$best.sha256" "$WORK/dl" || die "download of $src/$best.sha256 failed"
  T_DOWNLOAD=$(($(ms) - t))
  FILE="$WORK/dl/$best"
  log "downloaded $src/$best in $(secs $T_DOWNLOAD)s"
else
  best=""
  for tier in daily weekly monthly; do
    [[ -d $DEST/$tier ]] || continue
    n=$(find "$DEST/$tier" -maxdepth 1 -type f -printf '%f\n' | newest || true)
    if [[ -n $n && $n > ${best##*/} ]]; then best="$DEST/$tier/$n"; fi
  done
  [[ -n $best ]] || die "no backups found under $DEST/{daily,weekly,monthly}"
  FILE=$best
fi
BACKUP_NAME=$(basename "$FILE")
BACKUP_SIZE=$(stat -c %s "$FILE")
if [[ $BACKUP_NAME =~ ^btn_ops-([0-9]{4})([0-9]{2})([0-9]{2})-([0-9]{2})([0-9]{2})([0-9]{2})? ]]; then
  taken=$(TZ=$BTZ date -d "${BASH_REMATCH[1]}-${BASH_REMATCH[2]}-${BASH_REMATCH[3]} ${BASH_REMATCH[4]}:${BASH_REMATCH[5]}:${BASH_REMATCH[6]:-00}" +%s)
else taken=$(stat -c %Y "$FILE"); fi
BACKUP_AGE_H=$(awk -v s=$(($(date +%s) - taken)) 'BEGIN { printf "%.1f", s / 3600 }')
log "backup: $FILE ($BACKUP_SIZE bytes, ${BACKUP_AGE_H} h old)"

# ---- 2. checksum ----------------------------------------------------------------------------
t=$(ms)
[[ -f $FILE.sha256 ]] || die "checksum file missing: $FILE.sha256"
want=$(awk 'NR == 1 { print $1 }' "$FILE.sha256")
have=$(sha256sum -- "$FILE" | awk '{ print $1 }')
[[ $want =~ ^[0-9a-f]{64}$ && $want == "$have" ]] || die "checksum mismatch for $BACKUP_NAME"
T_VERIFY=$(($(ms) - t))
log "checksum ok"

# ---- 3. decrypt -----------------------------------------------------------------------------
gpg_run() {
  if [[ -n ${BACKUP_PASSPHRASE_FILE:-} ]]; then
    gpg --homedir "$WORK/gnupg" --batch --quiet --no-symkey-cache --pinentry-mode loopback \
      --passphrase-file "$BACKUP_PASSPHRASE_FILE" "$@"
  else
    gpg --homedir "$WORK/gnupg" --batch --quiet --no-symkey-cache --pinentry-mode loopback \
      --passphrase-fd 3 "$@" 3<<<"${BACKUP_PASSPHRASE:?set BACKUP_PASSPHRASE_FILE or BACKUP_PASSPHRASE}"
  fi
}
t=$(ms)
PLAIN="$WORK/restore.dump"
case $BACKUP_NAME in
  *.dump.gpg) gpg_run --decrypt --output "$PLAIN" "$FILE" || die "gpg decryption failed (wrong passphrase or damaged file)" ;;
  *.dump.age) command -v age >/dev/null || die "age not installed"
              age -d -i "${BACKUP_AGE_IDENTITY:?set BACKUP_AGE_IDENTITY to the age identity file}" -o "$PLAIN" "$FILE" \
                || die "age decryption failed" ;;
  *.dump) warn "backup is not encrypted"; cp -- "$FILE" "$PLAIN" ;;
esac
"$PG_RESTORE" --list "$PLAIN" >"$WORK/toc.txt" || die "decrypted file is not a readable pg_dump archive"
T_DECRYPT=$(($(ms) - t))
log "decrypt ok in $(secs $T_DECRYPT)s ($(grep -c 'TABLE DATA' "$WORK/toc.txt" || true) table-data entries)"

# ---- 4. restore into a scratch database -----------------------------------------------------
exists=$(admin_psql -c "SELECT 1 FROM pg_database WHERE datname = '$SCRATCH'") || die "cannot connect to the restore server"
[[ -z $exists ]] || die "database $SCRATCH already exists — refusing to touch it"
# template0: a pristine template, as pg_restore expects (and immune to local changes in template1).
admin_psql -c "CREATE DATABASE \"$SCRATCH\" TEMPLATE template0 ENCODING 'UTF8'" \
  || die "CREATE DATABASE $SCRATCH failed (role needs CREATEDB)"
CREATED=1
log "created scratch database $SCRATCH"
t=$(ms)
PGPASSWORD=$ADMIN_PW "$PG_RESTORE" --no-owner --no-acl --exit-on-error --jobs="$JOBS" \
  --dbname="$TARGET_CONN" "$PLAIN" 2>"$WORK/restore.err" \
  || die "pg_restore failed: $(tail -n 3 "$WORK/restore.err" | tr '\n' ' ')"
T_RESTORE=$(($(ms) - t))
log "restore ok in $(secs $T_RESTORE)s (jobs=$JOBS)"
rm -f -- "$PLAIN"

# ---- 5. sanity checks -----------------------------------------------------------------------
count_sql() {
  local t sql="SELECT 'goose', COALESCE(max(version_id), -1) FROM goose_db_version WHERE is_applied"
  for t in "${TABLES[@]}"; do
    sql+=" UNION ALL SELECT '$t', CASE WHEN to_regclass('public.\"$t\"') IS NULL THEN -1"
    sql+=" ELSE (xpath('/row/c/text()', query_to_xml('SELECT count(*) AS c FROM public.\"$t\"', false, true, '')))[1]::text::bigint END"
  done
  printf '%s' "$sql"
}
t=$(ms)
failed=0
while IFS='|' read -r k v; do
  if [[ $k == goose ]]; then GOOSE=$v; else RESTORED[$k]=$v; fi
done < <(target_psql -F '|' -c "$(count_sql)" || echo "query|failed")
[[ ${RESTORED[query]:-} != failed ]] || die "row-count query on the restored database failed"
for t2 in "${TABLES[@]}"; do
  if [[ ${RESTORED[$t2]:-} == -1 ]]; then RESTORED[$t2]=null; CHECK_NOTES+=("table $t2 missing in restored DB"); failed=1; fi
done
[[ ${GOOSE:--1} =~ ^[0-9]+$ && $GOOSE -gt 0 ]] || { CHECK_NOTES+=("goose_db_version empty"); failed=1; }
[[ ${RESTORED[User]:-0} =~ ^[0-9]+$ && ${RESTORED[User]} -gt 0 ]] || { CHECK_NOTES+=("no rows in \"User\""); failed=1; }
if ((COMPARE)); then
  while IFS='|' read -r k v; do
    if [[ $k == goose ]]; then LIVE_GOOSE=$v; else LIVE[$k]=$v; fi
  done < <(source_psql -F '|' -c "$(count_sql)" || echo "query|failed")
  [[ ${LIVE[query]:-} != failed ]] || die "row-count query on the live database failed"
  for t2 in "${TABLES[@]}"; do
    r=${RESTORED[$t2]} l=${LIVE[$t2]:-}
    [[ $l == -1 ]] && LIVE[$t2]=null && continue
    if [[ $r =~ ^[0-9]+$ && $l =~ ^[0-9]+$ ]]; then
      if ((l > r)); then CHECK_NOTES+=("$t2: live has $((l - r)) more rows than the backup (written since it was taken)")
      elif ((l < r)); then CHECK_NOTES+=("$t2: live has $((r - l)) FEWER rows than the backup — investigate deletions"); fi
    fi
  done
  [[ $LIVE_GOOSE == "$GOOSE" ]] || CHECK_NOTES+=("migration version differs: backup $GOOSE, live $LIVE_GOOSE")
fi
T_CHECKS=$(($(ms) - t))
((failed == 0)) || die "sanity checks failed: ${CHECK_NOTES[*]}"
log "sanity checks ok in $(secs $T_CHECKS)s (goose version $GOOSE, ${RESTORED[User]} users, ${RESTORED[AuditLog]} audit rows)"

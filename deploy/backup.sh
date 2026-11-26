#!/usr/bin/env bash
# BTN Operations — nightly encrypted PostgreSQL backup (SRS NFR-R1).
#
#   backup.sh [--env-file FILE]...      (default env file: /etc/btn-ops/backup.env, if it exists)
#
# What it does, in order:
#   1. pg_dump --format=custom of DATABASE_URL, streamed straight into the encryptor (no plaintext
#      dump ever touches the disk): age (BACKUP_AGE_RECIPIENT / _RECIPIENTS_FILE, public key) when
#      `age` is installed, else gpg --symmetric AES256 with BACKUP_PASSPHRASE_FILE (or BACKUP_PASSPHRASE,
#      passed on a file descriptor, never on the command line). Refuses to write an unencrypted
#      backup unless BACKUP_ALLOW_PLAINTEXT=1.
#   2. Verifies: decrypts to a pipe (full stream, so the cipher's integrity check runs) and runs
#      `pg_restore --list`; the archive must list the key tables. Writes <file>.sha256.
#   3. Files it under $BACKUP_DIR/daily, hard-links it into weekly/ (Sundays) and monthly/ (1st of the
#      month), then prunes each tier to its keep count (BACKUP_KEEP_DAILY/WEEKLY/MONTHLY).
#   4. Off-site: when BACKUP_RCLONE_REMOTE is set, `rclone copy` the file + checksum to
#      <remote>/<tier>/; prunes the remote tiers the same way when BACKUP_RCLONE_PRUNE=1.
#
# Uploaded files (logos) live in the database (table "asset"), so the dump is the whole backup.
# Schedule: deploy/btn-ops-backup.timer (01:30 Africa/Kigali). Restore drill: deploy/restore-test.sh.
# See deploy/backup.env.example for every setting.
#
# Exit codes: 0 ok · 1 config/usage error · 3 another run holds the lock · 4 dump/encrypt failed ·
#             5 verification failed · 6 off-site copy/prune failed (local backup is fine)
set -euo pipefail
umask 077

log()  { printf '%s [btn-backup] %s\n' "$(date -Is)" "$*"; }
warn() { printf '%s [btn-backup] WARN: %s\n' "$(date -Is)" "$*" >&2; }
die()  { local code=$1; shift; printf '%s [btn-backup] ERROR: %s\n' "$(date -Is)" "$*" >&2; exit "$code"; }

usage() { sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; }

# ---- configuration ---------------------------------------------------------------------------
load_env_file() {
  local f=$1 mode
  [[ -r $f ]] || die 1 "env file not readable: $f"
  mode=$(stat -c %a "$f" 2>/dev/null || echo 600)
  [[ $mode =~ ^[0-7]*[0-7][0-7]0$ ]] || warn "$f is accessible to other users (mode $mode); chmod 640 or 600 it"
  set -a
  # shellcheck disable=SC1090
  . "$f"
  set +a
}

ENV_FILES=()
while (($#)); do
  case $1 in
    --env-file) [[ $# -ge 2 ]] || die 1 "--env-file needs a path"; ENV_FILES+=("$2"); shift 2 ;;
    --env-file=*) ENV_FILES+=("${1#*=}"); shift ;;
    -h|--help) usage; exit 0 ;;
    *) die 1 "unknown argument: $1 (see --help)" ;;
  esac
done
if ((${#ENV_FILES[@]} == 0)) && [[ -f /etc/btn-ops/backup.env ]]; then ENV_FILES=(/etc/btn-ops/backup.env); fi
for f in "${ENV_FILES[@]}"; do load_env_file "$f"; done

: "${DATABASE_URL:?DATABASE_URL must be set (env or --env-file)}"
DEST="${BACKUP_DIR:-/var/backups/btn-ops}"
KEEP_DAILY="${BACKUP_KEEP_DAILY:-14}"
KEEP_WEEKLY="${BACKUP_KEEP_WEEKLY:-8}"
KEEP_MONTHLY="${BACKUP_KEEP_MONTHLY:-12}"
BTZ="${BACKUP_TZ:-Africa/Kigali}"
REMOTE="${BACKUP_RCLONE_REMOTE:-}"
for n in "$KEEP_DAILY" "$KEEP_WEEKLY" "$KEEP_MONTHLY"; do
  [[ $n =~ ^[0-9]+$ ]] || die 1 "BACKUP_KEEP_* must be non-negative integers (got '$n')"
done
((KEEP_DAILY >= 1)) || die 1 "BACKUP_KEEP_DAILY must be at least 1"

PG_DUMP=pg_dump PG_RESTORE=pg_restore
if [[ -n ${PG_BIN:-} ]]; then PG_DUMP="$PG_BIN/pg_dump" PG_RESTORE="$PG_BIN/pg_restore"; fi
command -v "$PG_DUMP" >/dev/null || die 1 "pg_dump not found (set PG_BIN)"
command -v "$PG_RESTORE" >/dev/null || die 1 "pg_restore not found (set PG_BIN)"

# Keep the password out of argv (visible in `ps`): split it from the URL into PGPASSWORD.
urldecode() { local s=${1//\\/\\\\}; printf '%b' "${s//%/\\x}"; }
DB_CONN=$DATABASE_URL
if [[ $DATABASE_URL =~ ^(postgres(ql)?://)([^:@/?#]*):([^@/?#]*)@(.*)$ ]]; then
  DB_CONN="${BASH_REMATCH[1]}${BASH_REMATCH[3]}@${BASH_REMATCH[5]}"
  PGPASSWORD=$(urldecode "${BASH_REMATCH[4]}")
  export PGPASSWORD
fi
redacted_db() { printf '%s' "$DB_CONN" | sed -E 's/(password=)[^ ]*/\1***/'; }

# ---- encryption ------------------------------------------------------------------------------
MODE=""
if [[ -n ${BACKUP_AGE_RECIPIENT:-}${BACKUP_AGE_RECIPIENTS_FILE:-} ]]; then
  if command -v age >/dev/null; then MODE=age
  else warn "BACKUP_AGE_RECIPIENT is set but 'age' is not installed; trying gpg"; fi
fi
if [[ -z $MODE && -n ${BACKUP_PASSPHRASE_FILE:-}${BACKUP_PASSPHRASE:-} ]]; then
  command -v gpg >/dev/null || die 1 "gpg not installed (needed for BACKUP_PASSPHRASE[_FILE])"
  if [[ -n ${BACKUP_PASSPHRASE_FILE:-} ]]; then
    [[ -r $BACKUP_PASSPHRASE_FILE && -s $BACKUP_PASSPHRASE_FILE ]] \
      || die 1 "BACKUP_PASSPHRASE_FILE missing, unreadable or empty: $BACKUP_PASSPHRASE_FILE"
  fi
  MODE=gpg
fi
if [[ -z $MODE ]]; then
  [[ ${BACKUP_ALLOW_PLAINTEXT:-0} == 1 ]] \
    || die 1 "no encryption configured: set BACKUP_AGE_RECIPIENT (with age installed) or BACKUP_PASSPHRASE_FILE (refusing to write a plaintext backup; BACKUP_ALLOW_PLAINTEXT=1 overrides)"
  MODE=plain
  warn "BACKUP_ALLOW_PLAINTEXT=1: writing an UNENCRYPTED backup"
fi
case $MODE in age) EXT=.dump.age ;; gpg) EXT=.dump.gpg ;; plain) EXT=.dump ;; esac

WORK=$(mktemp -d "${TMPDIR:-/tmp}/btn-backup.XXXXXX")
PART=""
cleanup() {
  [[ -n $PART && -e $PART ]] && rm -f -- "$PART"
  command -v gpgconf >/dev/null && gpgconf --homedir "$WORK/gnupg" --kill gpg-agent >/dev/null 2>&1 || true
  rm -rf -- "$WORK"
}
trap cleanup EXIT
mkdir -m 700 "$WORK/gnupg"

gpg_run() {   # throwaway GNUPGHOME: never touches ~/.gnupg, nothing cached in an agent
  if [[ -n ${BACKUP_PASSPHRASE_FILE:-} ]]; then
    gpg --homedir "$WORK/gnupg" --batch --yes --quiet --no-symkey-cache --pinentry-mode loopback \
      --passphrase-file "$BACKUP_PASSPHRASE_FILE" "$@"
  else
    gpg --homedir "$WORK/gnupg" --batch --yes --quiet --no-symkey-cache --pinentry-mode loopback \
      --passphrase-fd 3 "$@" 3<<<"$BACKUP_PASSPHRASE"
  fi
}
encrypt_to() {  # stdin -> $1
  case $MODE in
    age)
      local a=()
      [[ -n ${BACKUP_AGE_RECIPIENT:-} ]] && a+=(-r "$BACKUP_AGE_RECIPIENT")
      [[ -n ${BACKUP_AGE_RECIPIENTS_FILE:-} ]] && a+=(-R "$BACKUP_AGE_RECIPIENTS_FILE")
      age "${a[@]}" -o "$1" ;;
    gpg) gpg_run --symmetric --cipher-algo AES256 --s2k-digest-algo SHA512 --compress-algo none -o "$1" ;;
    plain) cat >"$1" ;;
  esac
}
can_decrypt() { [[ $MODE != age || -n ${BACKUP_AGE_IDENTITY:-} ]]; }
decrypt_cat() {  # $1 -> stdout
  case $MODE in
    age) age -d -i "$BACKUP_AGE_IDENTITY" "$1" ;;
    gpg) gpg_run --decrypt "$1" ;;
    plain) cat -- "$1" ;;
  esac
}

# ---- lock + names ----------------------------------------------------------------------------
mkdir -p "$DEST"/{daily,weekly,monthly}
exec 9>"$DEST/.backup.lock"
flock -n 9 || die 3 "another backup run holds $DEST/.backup.lock"

if [[ -n ${BACKUP_DATE:-} ]]; then NOW=$(TZ=$BTZ date -d "$BACKUP_DATE" +%s)   # testing hook
else NOW=$(date +%s); fi
STAMP=$(TZ=$BTZ date -d "@$NOW" +%Y%m%d-%H%M%S)
DOW=$(TZ=$BTZ date -d "@$NOW" +%u)
DOM=$(TZ=$BTZ date -d "@$NOW" +%d)
NAME="btn_ops-$STAMP$EXT"
FILE="$DEST/daily/$NAME"
[[ -e $FILE ]] && die 1 "$FILE already exists"
PART="$FILE.part"

shopt -s nullglob
legacy=("$DEST"/btn_ops-*.dump)
shopt -u nullglob
((${#legacy[@]})) && warn "${#legacy[@]} legacy unencrypted dump(s) in $DEST (old backup.sh); move off-site or delete"

# ---- 1. dump -> encrypt ----------------------------------------------------------------------
log "start: db=$(redacted_db) mode=$MODE file=$FILE"
t0=$(date +%s)
if ! "$PG_DUMP" --format=custom --compress=6 --dbname="$DB_CONN" | encrypt_to "$PART"; then
  die 4 "pg_dump or encryption failed"
fi
[[ -s $PART ]] || die 4 "backup file is empty"
t1=$(date +%s)
log "dump+encrypt ok in $((t1 - t0))s ($(du -h "$PART" | cut -f1))"

# ---- 2. verify -------------------------------------------------------------------------------
if can_decrypt; then
  # Drain the whole stream after pg_restore has read the TOC, so the cipher's MAC/MDC is checked.
  list_and_drain() { local rc=0; "$PG_RESTORE" --list >"$1" || rc=$?; cat >/dev/null; return "$rc"; }
  if ! decrypt_cat "$PART" | list_and_drain "$WORK/toc.txt"; then
    die 5 "verification failed: $PART does not decrypt to a readable pg_dump archive"
  fi
  entries=$(grep -c 'TABLE DATA' "$WORK/toc.txt" || true)
  for t in User Plant Product AuditLog goose_db_version; do
    grep -Eq "TABLE DATA public \"?$t\"? " "$WORK/toc.txt" || die 5 "verification failed: table $t missing from archive"
  done
  log "verify ok: decrypts, pg_restore --list reads $entries table-data entries"
else
  head -c 21 "$PART" | grep -q '^age-encryption.org/v1' || die 5 "verification failed: not an age file"
  warn "age mode without BACKUP_AGE_IDENTITY: only the header was checked; restore-test.sh does the full check"
fi
(cd "$DEST/daily" && sha256sum -- "$(basename "$PART")") | sed "s/\.part\$//" >"$FILE.sha256"
mv -- "$PART" "$FILE"
PART=""

# ---- 3. tiers + local retention ---------------------------------------------------------------
TIERS=(daily)
promote() {  # hard link (no extra space), copy across filesystems
  ln -f -- "$FILE" "$DEST/$1/$NAME" 2>/dev/null || cp -p -- "$FILE" "$DEST/$1/$NAME"
  cp -p -- "$FILE.sha256" "$DEST/$1/$NAME.sha256"
  TIERS+=("$1")
}
[[ $DOW == 7 ]] && ((KEEP_WEEKLY > 0)) && promote weekly
[[ $DOM == 01 ]] && ((KEEP_MONTHLY > 0)) && promote monthly

NAME_RE='^btn_ops-[0-9]{8}-[0-9]{4,6}\.dump(\.gpg|\.age)?$'
prune_local() {
  local tier=$1 keep=$2 f
  local -a names
  mapfile -t names < <(find "$DEST/$tier" -maxdepth 1 -type f -printf '%f\n' | grep -E "$NAME_RE" | sort -r)
  for f in "${names[@]:keep}"; do
    rm -f -- "$DEST/$tier/$f" "$DEST/$tier/$f.sha256"
    log "pruned $tier/$f"
  done
}
prune_local daily "$KEEP_DAILY"
prune_local weekly "$KEEP_WEEKLY"
prune_local monthly "$KEEP_MONTHLY"
log "backup ok: $FILE tiers=${TIERS[*]}"

# ---- 4. off-site -----------------------------------------------------------------------------
if [[ -n $REMOTE ]]; then
  command -v rclone >/dev/null || die 6 "BACKUP_RCLONE_REMOTE is set but rclone is not installed"
  read -r -a RFLAGS <<<"${BACKUP_RCLONE_FLAGS:-}"
  rjoin() { case $1 in *: | */) printf '%s%s' "$1" "$2" ;; *) printf '%s/%s' "$1" "$2" ;; esac; }
  for tier in "${TIERS[@]}"; do
    dst=$(rjoin "$REMOTE" "$tier")
    rclone copy "${RFLAGS[@]}" --no-traverse "$FILE" "$dst" || die 6 "rclone copy to $dst failed"
    rclone copy "${RFLAGS[@]}" --no-traverse "$FILE.sha256" "$dst" || die 6 "rclone copy of checksum to $dst failed"
    log "off-site ok: $dst/$NAME"
  done
  if [[ ${BACKUP_RCLONE_PRUNE:-0} == 1 ]]; then
    prune_remote() {
      local tier=$1 keep=$2 dst n
      local -a names
      dst=$(rjoin "$REMOTE" "$tier")
      mapfile -t names < <(rclone lsf "${RFLAGS[@]}" --files-only "$dst/" 2>/dev/null | grep -E "$NAME_RE" | sort -r)
      for n in "${names[@]:keep}"; do
        rclone deletefile "${RFLAGS[@]}" "$dst/$n" || die 6 "rclone deletefile $dst/$n failed"
        rclone deletefile "${RFLAGS[@]}" "$dst/$n.sha256" 2>/dev/null || true
        log "pruned off-site $tier/$n"
      done
    }
    prune_remote daily "$KEEP_DAILY"
    prune_remote weekly "$KEEP_WEEKLY"
    prune_remote monthly "$KEEP_MONTHLY"
  fi
else
  warn "BACKUP_RCLONE_REMOTE not set: no off-site copy made"
fi
log "done in $(($(date +%s) - t0))s"

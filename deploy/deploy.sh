#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
#  BTN Operations — one-shot production deploy (Ubuntu/Debian, systemd, nginx, PostgreSQL)
#
#  • Web : https://operations.btnlimited.com     → nginx serves frontend/dist and proxies /api/* to the Go API
#                                                   (same origin: the session cookies are scoped to /api/auth)
#  • API : https://operation-api.btnlimited.com  → nginx proxies everything to the Go API (integrations,
#                                                   /api/v1/openapi.json, /api/v1/docs)
#  • Go  : static binaries built from backend/ (server, migrate, seed, import), systemd unit btn-ops-api,
#          on 127.0.0.1:<PORT> behind nginx, migrations applied before every start
#  • Code: synced to APP_DIR (default /opt/btn-ops, the path the backup units expect)
#
#  Usage (on the server, from a checkout of this repository, as a sudoer):
#     sudo bash deploy/deploy.sh --install-packages # first run: everything (tools, DB, API, web, nginx, TLS, backups)
#     sudo bash deploy/deploy.sh                    # update (idempotent)
#     sudo bash deploy/deploy.sh --backend-only     # Go API only (skip the frontend build)
#     sudo bash deploy/deploy.sh --frontend-only    # rebuild + publish the web app only
#     sudo bash deploy/deploy.sh --no-certbot       # don't request TLS certificates
#     sudo bash deploy/deploy.sh --seed             # run the (idempotent) seed again
#     sudo bash deploy/deploy.sh --force-build      # rebuild the web app even when the sources are unchanged
#     sudo bash deploy/deploy.sh --install-packages # allow apt-get for missing tools (nginx, psql, go, node…)
#     sudo bash deploy/deploy.sh --set-db-password  # change the password of an EXISTING database role to the
#                                                   # one in DATABASE_URL (off by default)
#  Overrides:  WEB_DOMAIN=… API_DOMAIN=… APP_DIR=… RUN_USER=… CERTBOT_EMAIL=…
#
#  Production env: backend/.env.p (gitignored — copy it onto the server once: scp backend/.env.p server:…/backend/).
#  It is applied to $APP_DIR/backend/.env on the first run and again whenever .env.p changes. Domains, APP_ENV,
#  PORT, TRUSTED_PROXIES and WEBAUTHN_* are forced by this script; OTP_PEPPER is generated when missing.
#  The Let's Encrypt e-mail is CERTBOT_EMAIL, else OPS_ALERT_EMAIL / SUPER_ADMIN_EMAIL from the env file.
#  DNS A records for both domains must point here.
#
#  Scope: only btn-ops-api.service, btn-ops-backup.{service,timer}, the two nginx sites,
#  /etc/nginx/conf.d/btn-ops.conf, /etc/btn-ops/, the database from DATABASE_URL and /usr/local/bin/btnops
#  are touched. nginx is reloaded, never restarted.
#  Afterwards:  btnops status | logs | restart | update | help
# ─────────────────────────────────────────────────────────────────────────────
set -Eeuo pipefail
trap 'printf "\e[1;31m✖ deploy failed at line %s: %s\e[0m\n" "$LINENO" "$BASH_COMMAND" >&2; echo "   (rerun with --verbose for a full trace; btnops logs shows the API journal)" >&2' ERR

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
APP_DIR="${APP_DIR:-/opt/btn-ops}"
WEB_DOMAIN="${WEB_DOMAIN:-operations.btnlimited.com}"
API_DOMAIN="${API_DOMAIN:-operation-api.btnlimited.com}"
SERVICE="btn-ops-api"
RUN_USER="${RUN_USER:-btnops}"
CONF_DIR=/etc/btn-ops
CONF_FILE="$CONF_DIR/deploy.conf"
CERTBOT_EMAIL="${CERTBOT_EMAIL:-}"
[[ -z "$CERTBOT_EMAIL" && -r "$CONF_FILE" ]] && CERTBOT_EMAIL="$(sed -n 's/^CERTBOT_EMAIL=//p' "$CONF_FILE")"
STATE_DIR=/var/lib/btn-ops-deploy
DATA_DIR=/var/lib/btn-ops
ACME_ROOT=/var/www/html
BACKEND="$APP_DIR/backend"
FRONTEND="$APP_DIR/frontend"
ENV_FILE="$BACKEND/.env"

DO_BACKEND=1; DO_FRONTEND=1; DO_CERTBOT=1; INSTALL_PKGS=0; SET_DB_PASSWORD=0; DO_SEED=0; FORCE_BUILD=0
for a in "$@"; do case "$a" in
  --backend-only) DO_FRONTEND=0;; --frontend-only) DO_BACKEND=0;; --no-certbot) DO_CERTBOT=0;;
  --install-packages) INSTALL_PKGS=1;; --set-db-password) SET_DB_PASSWORD=1;; --seed) DO_SEED=1;;
  --force-build) FORCE_BUILD=1;; --verbose|-v) set -x;;
  -h|--help) sed -n 2,35p "$0"; exit 0;; *) echo "unknown option $a (see --help)"; exit 1;; esac; done

WARNINGS=()
log()  { printf '\e[1;34m▶ %s\e[0m\n' "$*"; }
ok()   { printf '\e[1;32m✔ %s\e[0m\n' "$*"; }
warn() { printf '\e[1;33m! %s\e[0m\n' "$*"; WARNINGS+=("$*"); }
die()  { printf '\e[1;31m✖ %s\e[0m\n' "$*" >&2; exit 1; }
apt_install() {
  ((INSTALL_PKGS)) || die "missing: $* — install it (apt-get install $*) or rerun with --install-packages"
  log "apt-get install $*"
  NEEDRESTART_SUSPEND=1 NEEDRESTART_MODE=l DEBIAN_FRONTEND=noninteractive apt-get install -y -qq -o Dpkg::Options::=--force-confold "$@"
}
# find_tool NAME [extra dirs…] — PATH first (sudo resets it), then the usual install locations
find_tool() {
  local name="$1" d home=""; shift
  command -v "$name" 2>/dev/null && return 0
  [[ -n "${SUDO_USER:-}" ]] && home="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
  for d in "$@" /usr/local/bin /usr/bin /snap/bin ${home:+"$home/.local/bin" "$home/go/bin" "$home/.local/share/pnpm"}; do
    [[ -x "$d/$name" ]] && { echo "$d/$name"; return 0; }
  done
  if [[ -n "$home" && "$name" == node ]]; then  # nvm / fnm installs
    d="$(find "$home/.nvm/versions/node" "$home/.local/share/fnm" -path '*/bin/node' -type f 2>/dev/null | sort -V | tail -1)"
    [[ -n "$d" ]] && { echo "$d"; return 0; }
  fi
  return 1
}
port_busy() { ss -Hltn "sport = :$1" 2>/dev/null | grep -q .; }

[[ $EUID -eq 0 ]] || die "run with sudo"
printf '\e[1m\n═══ BTN Operations — deploy ═══\e[0m\n'
echo "   source  : $SRC_DIR"
echo "   app dir : $APP_DIR      service: $SERVICE (user $RUN_USER)"
echo "   web     : https://$WEB_DOMAIN"
echo "   api     : https://$API_DOMAIN"
echo

# ── 1. tools ────────────────────────────────────────────────────────────────
log "Checking required tools (nothing is installed unless --install-packages)"
need=()
command -v nginx   >/dev/null || need+=(nginx)
command -v psql    >/dev/null || need+=(postgresql-client)
command -v curl    >/dev/null || need+=(curl)
command -v openssl >/dev/null || need+=(openssl)
command -v rsync   >/dev/null || need+=(rsync)
command -v ss      >/dev/null || need+=(iproute2)
if ((DO_CERTBOT)) && ! command -v certbot >/dev/null; then need+=(certbot); fi
((${#need[@]})) && apt_install "${need[@]}"
for svc in nginx postgresql; do
  if systemctl is-active --quiet "$svc"; then ok "$svc is running"; else warn "$svc is not active on this host (systemctl start $svc)"; fi
done

GO=""
if ((DO_BACKEND)); then
  GO="$(find_tool go /usr/local/go/bin /usr/lib/go/bin)" || { apt_install golang-go; GO="$(find_tool go)"; } || die "Go not found"
  # go.mod asks for Go 1.26; any Go >= 1.21 downloads that toolchain itself (GOTOOLCHAIN=auto)
  GOVER="$( { "$GO" env GOVERSION; "$GO" env | sed -n "s/^GOVERSION=//p"; "$GO" version; } 2>/dev/null | grep -oE 'go[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1 || true)"
  if [[ -z "$GOVER" ]]; then
    warn "could not read the Go version of $GO — trying the build anyway"
  else
    read -r gomaj gomin <<<"$(sed -E 's/^go([0-9]+)\.([0-9]+).*/\1 \2/' <<<"$GOVER")"
    (( gomaj > 1 || (gomaj == 1 && gomin >= 21) )) || die "Go >= 1.21 is required to build the API (found $GOVER at $GO)"
  fi
  ok "go: $GO (${GOVER:-version unknown})"
fi
NODE=""; PNPM=""
if ((DO_FRONTEND)); then
  NODE="$(find_tool node)" || true
  if [[ -z "$NODE" ]] || (( $("$NODE" -v | cut -c2- | cut -d. -f1) < 20 )); then
    ((INSTALL_PKGS)) || die "Node.js 20+ is required to build the web app (found: ${NODE:-none}). Install it or rerun with --install-packages"
    log "Installing Node.js 22 (NodeSource)"; curl -fsSL https://deb.nodesource.com/setup_22.x | NEEDRESTART_SUSPEND=1 bash - >/dev/null
    apt_install nodejs; NODE="$(command -v node)"
  fi
  PATH="$(dirname "$NODE"):$PATH"; export PATH
  NODE_BIN="$(dirname "$NODE")"
  # pnpm: already installed → corepack (ships with Node) → npm i -g → apt's npm (Debian's nodejs has no npm/npx)
  PNPM="$(find_tool pnpm "$NODE_BIN")" || PNPM=""
  if [[ -z "$PNPM" ]] && COREPACK="$(find_tool corepack "$NODE_BIN")"; then
    "$COREPACK" enable pnpm >/dev/null 2>&1 && "$COREPACK" prepare pnpm@10 --activate >/dev/null 2>&1 || true
    PNPM="$(find_tool pnpm "$NODE_BIN")" || PNPM=""
  fi
  if [[ -z "$PNPM" ]]; then
    NPM="$(find_tool npm "$NODE_BIN")" || NPM=""
    if [[ -z "$NPM" ]] && ((INSTALL_PKGS)); then apt_install npm; NPM="$(find_tool npm "$NODE_BIN")" || NPM=""; fi
    if [[ -n "$NPM" ]]; then
      log "Installing pnpm 10 (npm install -g pnpm@10)"
      "$NPM" install -g --silent pnpm@10 >/dev/null 2>&1 || true
      PNPM="$(find_tool pnpm "$NODE_BIN" "$("$NPM" prefix -g 2>/dev/null)/bin")" || PNPM=""
    fi
  fi
  [[ -n "$PNPM" ]] || die "pnpm is required to build the web app and could not be installed.
   Install it (npm install -g pnpm@10, or: apt-get install npm && npm install -g pnpm@10) or rerun with --install-packages"
  ok "node: $NODE ($("$NODE" -v))  pnpm: $PNPM ($("$PNPM" --version 2>/dev/null))"
fi

# ── 2. service user + directories ───────────────────────────────────────────
if ! id "$RUN_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$DATA_DIR" --no-create-home --shell /usr/sbin/nologin "$RUN_USER"
  ok "created system user $RUN_USER"
fi
install -d -m 755 "$APP_DIR" "$STATE_DIR" "$ACME_ROOT"
install -d -m 750 -o "$RUN_USER" -g "$RUN_USER" "$DATA_DIR"
install -d -m 750 -o root -g "$RUN_USER" "$CONF_DIR"

# ── 3. code ─────────────────────────────────────────────────────────────────
if [[ "$SRC_DIR" != "$APP_DIR" ]]; then
  log "Syncing code $SRC_DIR → $APP_DIR"
  rsync -a --delete --exclude .git --exclude node_modules --exclude 'frontend/dist' --exclude 'backend/bin' \
        --exclude 'backend/.env' --exclude 'backend/.env.*' --exclude coverage --exclude '*.log' "$SRC_DIR/" "$APP_DIR/"
fi
APP_VERSION="$(git -c safe.directory="$SRC_DIR" -C "$SRC_DIR" describe --always --dirty 2>/dev/null || true)"

# ── 4. environment file (backend/.env) ──────────────────────────────────────
envget() { { grep -E "^$1=" "$ENV_FILE" || true; } | tail -1 | cut -d= -f2- | sed -E 's/^"(.*)"$/\1/; s/^'"'"'(.*)'"'"'$/\1/'; }
# production env source: backend/.env.p (gitignored), else .env.production; applied when new or changed
PROD_ENV=""; for f in "$SRC_DIR/backend/.env.p" "$SRC_DIR/backend/.env.production"; do [[ -f "$f" ]] && { PROD_ENV="$f"; break; }; done
if [[ -n "$PROD_ENV" ]]; then
  PROD_SHA="$(sha1sum "$PROD_ENV" | cut -d' ' -f1)"
  if [[ ! -f "$ENV_FILE" || "$(cat "$STATE_DIR/env.sha" 2>/dev/null)" != "$PROD_SHA" ]]; then
    OLD_PEPPER=""
    if [[ -f "$ENV_FILE" ]]; then OLD_PEPPER="$(envget OTP_PEPPER)"; cp -p "$ENV_FILE" "$ENV_FILE.bak.$(date +%Y%m%d-%H%M%S)"; fi
    cp "$PROD_ENV" "$ENV_FILE"
    # keep a pepper generated earlier when .env.p has none (changing it would invalidate pending codes)
    if [[ -n "$OLD_PEPPER" ]] && [[ -z "$(envget OTP_PEPPER)" || "$(envget OTP_PEPPER)" == *CHANGE_ME* ]]; then
      sed -i '/^OTP_PEPPER=/d' "$ENV_FILE"; printf 'OTP_PEPPER="%s"\n' "$OLD_PEPPER" >> "$ENV_FILE"
    fi
    echo "$PROD_SHA" > "$STATE_DIR/env.sha"
    ok "applied $(basename "$PROD_ENV") → $ENV_FILE"
  fi
elif [[ ! -f "$ENV_FILE" && -f "$SRC_DIR/backend/.env" ]]; then
  cp "$SRC_DIR/backend/.env" "$ENV_FILE"; warn "no backend/.env.p — started from the development backend/.env"
fi
[[ -f "$ENV_FILE" ]] || die "no production env file. Copy backend/.env.p from your computer into $SRC_DIR/backend/ and rerun:
       scp backend/.env.p <server>:$SRC_DIR/backend/.env.p
   (or create it from backend/.env.example: DATABASE_URL with host 127.0.0.1, SMTP_*, SUPER_ADMIN_*)"
envset() {  # envset KEY VALUE — writes KEY="VALUE" (replacing an existing line)
  local v="${2//\\/\\\\}"; v="${v//&/\\&}"; v="${v//|/\\|}"
  if grep -qE "^$1=" "$ENV_FILE"; then sed -i "s|^$1=.*|$1=\"$v\"|" "$ENV_FILE"; else printf '%s="%s"\n' "$1" "$2" >> "$ENV_FILE"; fi
}
placeholder() { [[ -z "$1" || "$1" == *CHANGE_ME* || "$1" == *example.com* ]]; }

# port: keep the one this deploy already uses, otherwise PORT from .env (default 4000) if free, else a free one
API_PORT="$(envget PORT)"; API_PORT="${API_PORT:-4000}"
PREV_PORT="$(cat "$STATE_DIR/api_port" 2>/dev/null || true)"
if [[ "$API_PORT" != "$PREV_PORT" ]] && port_busy "$API_PORT"; then
  WANTED="$API_PORT"
  for p in $(seq 4001 4099) $(shuf -i 20000-60000 -n 50); do port_busy "$p" || { API_PORT="$p"; break; }; done
  warn "port $WANTED is used by another program — the API uses $API_PORT instead"
fi
echo "$API_PORT" > "$STATE_DIR/api_port"

# values that follow from the domains and the nginx topology (kept in sync on every deploy)
envset APP_ENV production
envset AUTH_DEV_LOG_OTP false
envset PORT "$API_PORT"
envset APP_URL "https://$WEB_DOMAIN"
envset FRONTEND_ORIGIN "https://$WEB_DOMAIN"
envset WEBAUTHN_RP_ID "$WEB_DOMAIN"
envset WEBAUTHN_ORIGINS "https://$WEB_DOMAIN"
envset TRUSTED_PROXIES "127.0.0.1,::1"
envset DATA_DIR "$DATA_DIR"
[[ -n "$APP_VERSION" ]] && envset APP_VERSION "$APP_VERSION"
PEPPER="$(envget OTP_PEPPER)"
if placeholder "$PEPPER" || (( ${#PEPPER} < 32 )); then envset OTP_PEPPER "$(openssl rand -hex 32)"; ok "generated OTP_PEPPER"; fi
chown root:"$RUN_USER" "$ENV_FILE"; chmod 640 "$ENV_FILE"
# Let's Encrypt contact: CERTBOT_EMAIL, else the alert / administrator address from the env file
for k in OPS_ALERT_EMAIL SUPER_ADMIN_EMAIL SMTP_USER; do
  [[ -n "$CERTBOT_EMAIL" ]] && break
  v="$(envget "$k")"
  if [[ "$v" == *@* ]] && ! placeholder "$v"; then CERTBOT_EMAIL="$v"; fi
done

# checks that would otherwise only fail at runtime
DATABASE_URL="$(envget DATABASE_URL)"
[[ -n "$DATABASE_URL" ]] || die "DATABASE_URL is empty in $ENV_FILE"
if [[ "$DATABASE_URL" =~ ^postgres(ql)?\+[a-z0-9]+:// ]]; then  # SQLAlchemy style (postgresql+psycopg://) → plain URL for Go
  DATABASE_URL="postgresql://${DATABASE_URL#*://}"; envset DATABASE_URL "$DATABASE_URL"; ok "DATABASE_URL: dropped the SQLAlchemy driver suffix"
fi
[[ "$DATABASE_URL" != *CHANGE_ME* ]] || die "DATABASE_URL in $ENV_FILE still contains CHANGE_ME — put the real database password there"
urldecode() { local s="${1//+/ }"; printf '%b' "${s//%/\\x}"; }
if [[ "$DATABASE_URL" =~ ^postgres(ql)?://([^:@/]+)(:([^@]*))?@([^:/?]+)(:([0-9]+))?/([^?]+) ]]; then
  DB_USER="$(urldecode "${BASH_REMATCH[2]}")"; DB_PASS="$(urldecode "${BASH_REMATCH[4]}")"
  DB_HOST="${BASH_REMATCH[5]}"; DB_PORT="${BASH_REMATCH[7]:-5432}"; DB_NAME="$(urldecode "${BASH_REMATCH[8]}")"
else
  die "DATABASE_URL in $ENV_FILE is not postgresql://user:password@host:port/dbname?…"
fi
[[ "$DB_HOST" != db ]] || die "DATABASE_URL uses host 'db' (the docker-compose name) — use 127.0.0.1 on this server"
(( ${#DB_PASS} >= 10 )) || die "the database password in DATABASE_URL is shorter than 10 characters — use a stronger one"
if placeholder "$(envget SMTP_HOST)" || placeholder "$(envget SMTP_PASSWORD)"; then
  warn "SMTP_HOST/SMTP_PASSWORD in $ENV_FILE are placeholders — sign-in codes cannot be emailed, so nobody can sign in"
fi
if placeholder "$(envget MAIL_FROM)"; then warn "MAIL_FROM in $ENV_FILE is still a placeholder"; fi
ok "env file: $ENV_FILE (db $DB_NAME as $DB_USER @ $DB_HOST:$DB_PORT, API port $API_PORT)"

# ── 5. database role + database ─────────────────────────────────────────────
log "Checking PostgreSQL access for role '$DB_USER'"
pg_try()   { PGPASSWORD="$DB_PASS" psql -X -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$1" -w -tAc "select 1" >/dev/null 2>&1; }
pg_super() { sudo -u postgres psql -X -w -q -v ON_ERROR_STOP=1 -v u="$DB_USER" -v pw="$DB_PASS" -v db="$DB_NAME" "$@"; }
LOCAL_DB=0
if [[ "$DB_HOST" == 127.0.0.1 || "$DB_HOST" == localhost || "$DB_HOST" == ::1 ]] && pg_super -tAc "select 1" >/dev/null 2>&1; then LOCAL_DB=1; fi
if ! pg_try postgres && ! pg_try "$DB_NAME"; then
  ((LOCAL_DB)) || die "PostgreSQL at $DB_HOST:$DB_PORT rejects $DB_USER with the password from DATABASE_URL (and it is not a local server this script can administer)"
  if [[ -z "$(pg_super -tA <<<"SELECT 1 FROM pg_roles WHERE rolname = :'u'")" ]]; then
    pg_super <<<"SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', :'u', :'pw') \gexec"
    ok "created database role '$DB_USER'"
  elif ((SET_DB_PASSWORD)); then
    pg_super <<<"SELECT format('ALTER ROLE %I WITH PASSWORD %L', :'u', :'pw') \gexec"
    ok "password of existing role '$DB_USER' changed (--set-db-password)"
  else
    die "role '$DB_USER' exists but rejects the password in DATABASE_URL. Put that role's real password in $ENV_FILE,
   use another role name in DATABASE_URL (it is created automatically), or rerun with --set-db-password."
  fi
fi
if ((LOCAL_DB)) && [[ -z "$(pg_super -tA <<<"SELECT 1 FROM pg_database WHERE datname = :'db'")" ]]; then
  pg_super <<<"SELECT format('CREATE DATABASE %I OWNER %I TEMPLATE template0 ENCODING %L', :'db', :'u', 'UTF8') \gexec"
  ok "created database '$DB_NAME' (owner $DB_USER)"; DO_SEED=1
elif ! pg_try "$DB_NAME" && pg_try postgres; then  # no local admin access: create it with the DATABASE_URL role (needs CREATEDB)
  if PGPASSWORD="$DB_PASS" psql -X -w -q -v ON_ERROR_STOP=1 -v db="$DB_NAME" -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d postgres \
       <<<"SELECT format('CREATE DATABASE %I TEMPLATE template0 ENCODING %L', :'db', 'UTF8') \gexec"; then
    ok "created database '$DB_NAME' as $DB_USER"; DO_SEED=1
  else die "could not create database '$DB_NAME' as $DB_USER (needs CREATEDB)"; fi
fi
pg_try "$DB_NAME" || die "cannot connect to database '$DB_NAME' as $DB_USER@$DB_HOST:$DB_PORT (does it exist? does pg_hba.conf allow scram/md5 from $DB_HOST?)"
ok "PostgreSQL connection OK"

# ── 6. Go API: build → migrate → (seed) → systemd ───────────────────────────
if ((DO_BACKEND)); then
  log "Building the Go API ($BACKEND/bin)"
  ( cd "$BACKEND" && CGO_ENABLED=0 GOTOOLCHAIN="${GOTOOLCHAIN:-auto}" "$GO" build -buildvcs=false -trimpath -tags timetzdata \
      -ldflags="-s -w" -o bin/ ./cmd/server ./cmd/migrate ./cmd/seed ./cmd/import )
  chmod 755 "$BACKEND"/bin/*
  ok "built server, migrate, seed, import"

  log "Applying database migrations"
  ( cd "$BACKEND" && sudo -u "$RUN_USER" ./bin/migrate up ) || die "migrate up failed (see above)"
  ok "migrations applied"

  [[ -f "$STATE_DIR/seeded" ]] || DO_SEED=1
  if ((DO_SEED)); then
    placeholder "$(envget SUPER_ADMIN_EMAIL)" && die "set SUPER_ADMIN_EMAIL in $ENV_FILE (the first administrator) before the first deploy"
    SA_PW="$(envget SUPER_ADMIN_PASSWORD)"
    [[ -n "$SA_PW" && "$SA_PW" != *CHANGE_ME* ]] || die "set SUPER_ADMIN_PASSWORD in $ENV_FILE (the first administrator's password) before the first deploy"
    log "Seeding (plants, catalogue, settings, branding, super administrator — idempotent)"
    ( cd "$BACKEND" && sudo -u "$RUN_USER" ./bin/seed ) || die "seed failed (see above)"
    touch "$STATE_DIR/seeded"; ok "seed complete"
  fi

  log "Writing /etc/systemd/system/$SERVICE.service"
  sed -e "s|/opt/btn-ops|$APP_DIR|g" -e "s|^User=.*|User=$RUN_USER|" -e "s|^Group=.*|Group=$RUN_USER|" \
      "$APP_DIR/deploy/btn-ops-api.service" > "/etc/systemd/system/$SERVICE.service"
  if [[ "$APP_DIR" == /home/* || "$APP_DIR" == /root/* ]]; then sed -i '/^ProtectHome=/d' "/etc/systemd/system/$SERVICE.service"; fi
  systemctl daemon-reload
  systemctl enable "$SERVICE" >/dev/null 2>&1
  systemctl restart "$SERVICE" || true
  printf '   waiting for http://127.0.0.1:%s/api/health ' "$API_PORT"
  for _ in $(seq 1 45); do
    sleep 1; printf '.'
    curl -fs "http://127.0.0.1:$API_PORT/api/health" >/dev/null 2>&1 && break
    systemctl is-failed --quiet "$SERVICE" && break
  done; echo
  if curl -fs "http://127.0.0.1:$API_PORT/api/health" >/dev/null 2>&1; then ok "$SERVICE is running on port $API_PORT"
  else echo "── last journal lines of $SERVICE ──"; journalctl -u "$SERVICE" -n 60 --no-pager -o cat || true; die "$SERVICE is not answering on port $API_PORT (journal above)"; fi
fi

# ── 7. web app (frontend/dist) ──────────────────────────────────────────────
if ((DO_FRONTEND)); then
  BUILD_SHA="$( { find "$FRONTEND/src" "$FRONTEND/src-sw" "$FRONTEND/public" -type f -print0 2>/dev/null | sort -z | xargs -0 sha1sum
                  sha1sum "$FRONTEND"/{package.json,pnpm-lock.yaml,index.html,vite.config.ts} 2>/dev/null; } | sha1sum | cut -d' ' -f1 )"
  if ((! FORCE_BUILD)) && [[ -f "$FRONTEND/dist/index.html" && "$(cat "$STATE_DIR/frontend.sha" 2>/dev/null)" == "$BUILD_SHA" ]]; then
    ok "web app unchanged since the last deploy — build skipped (--force-build to rebuild)"
  else
    log "Building the web app"
    ( cd "$FRONTEND" && CI=true "$PNPM" install --frozen-lockfile --reporter=silent && CI=true "$PNPM" run build )
    echo "$BUILD_SHA" > "$STATE_DIR/frontend.sha"
    ok "web app built → $FRONTEND/dist"
  fi
  chmod -R a+rX "$FRONTEND/dist"
fi
[[ -f "$FRONTEND/dist/index.html" ]] || warn "$FRONTEND/dist is missing — run: sudo bash deploy/deploy.sh --frontend-only"

# ── 8. nginx ────────────────────────────────────────────────────────────────
log "Writing nginx configuration (our two sites only)"
# shared http-level pieces: upstream + edge rate limit for the sign-in endpoints
{
  echo "# managed by $APP_DIR/deploy/deploy.sh — BTN Operations"
  # shellcheck disable=SC2016  # $binary_remote_addr is an nginx variable
  grep -rqs --exclude=btn-ops.conf 'zone=btn_auth:' /etc/nginx/ || echo 'limit_req_zone $binary_remote_addr zone=btn_auth:10m rate=300r/m;'
  echo "upstream btn_ops_api { server 127.0.0.1:$API_PORT; keepalive 16; }"
} > /etc/nginx/conf.d/btn-ops.conf
NGINX_VER="$(nginx -v 2>&1 | sed -nE 's#.*nginx/([0-9]+)\.([0-9]+)\.([0-9]+).*#\1 \2 \3#p')"
read -r nmaj nmin npatch <<<"${NGINX_VER:-1 18 0}"
NEW_HTTP2=0; if (( nmaj > 1 || nmin > 25 || (nmin == 25 && npatch >= 1) )); then NEW_HTTP2=1; fi
render() {  # render TEMPLATE DOMAIN — TLS listen + HTTP→HTTPS redirect once the certificate exists
  local tpl="$1" d="$2" live="/etc/letsencrypt/live/$2" listen redirect=""
  if [[ -f "$live/fullchain.pem" ]]; then
    if ((NEW_HTTP2)); then listen="    listen 443 ssl;\n    listen [::]:443 ssl;\n    http2 on;"
    else listen="    listen 443 ssl http2;\n    listen [::]:443 ssl http2;"; fi
    listen+="\n    ssl_certificate $live/fullchain.pem;\n    ssl_certificate_key $live/privkey.pem;\n    ssl_protocols TLSv1.2 TLSv1.3;"
    [[ -f /etc/letsencrypt/options-ssl-nginx.conf ]] && listen+="\n    include /etc/letsencrypt/options-ssl-nginx.conf;"
    [[ -f /etc/letsencrypt/ssl-dhparams.pem ]] && listen+="\n    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;"
    redirect="# HTTP → HTTPS (ACME challenges still answered so renewals work)\nserver {\n    listen 80;\n    listen [::]:80;\n    server_name $d;\n    location ^~ /.well-known/acme-challenge/ { root $ACME_ROOT; default_type text/plain; }\n    location / { return 301 https://\$host\$request_uri; }\n}"
  else
    listen="    listen 80;\n    listen [::]:80;"
  fi
  sed -e "s|@APP_DIR@|$APP_DIR|g" -e "s|@WEB_DOMAIN@|$WEB_DOMAIN|g" -e "s|@API_DOMAIN@|$API_DOMAIN|g" -e "s|@ACME_ROOT@|$ACME_ROOT|g" "$tpl" \
    | awk -v L="$listen" -v R="$redirect" '{ if ($0=="@LISTEN@") { gsub(/\\n/,"\n",L); print L } else if ($0=="@REDIRECT@") { gsub(/\\n/,"\n",R); print R } else print }'
}
write_sites() {
  render "$APP_DIR/deploy/nginx-web.conf" "$WEB_DOMAIN" > "/etc/nginx/sites-available/$WEB_DOMAIN"
  render "$APP_DIR/deploy/nginx-api.conf" "$API_DOMAIN" > "/etc/nginx/sites-available/$API_DOMAIN"
  ln -sf "/etc/nginx/sites-available/$WEB_DOMAIN" "/etc/nginx/sites-enabled/$WEB_DOMAIN"
  ln -sf "/etc/nginx/sites-available/$API_DOMAIN" "/etc/nginx/sites-enabled/$API_DOMAIN"
  if ! nginx -t >/dev/null 2>&1; then nginx -t || true; die "nginx -t failed (output above) — nginx was NOT reloaded"; fi
  systemctl reload nginx
}
write_sites
for d in "$WEB_DOMAIN" "$API_DOMAIN"; do
  if [[ -f "/etc/letsencrypt/live/$d/fullchain.pem" ]]; then ok "$d: HTTPS + redirect from HTTP"; else echo "   $d: HTTP only until a certificate exists"; fi
done

# ── 9. TLS (Let's Encrypt, webroot — certbot never edits our site files) ────
TLS_MISSING=()
if ((DO_CERTBOT)) && command -v certbot >/dev/null; then
  for d in "$WEB_DOMAIN" "$API_DOMAIN"; do
    if [[ -f "/etc/letsencrypt/live/$d/fullchain.pem" ]]; then ok "certificate present for $d"; continue; fi
    if [[ -z "$CERTBOT_EMAIL" ]]; then
      warn "no certificate for $d: no e-mail for Let's Encrypt (set OPS_ALERT_EMAIL in backend/.env.p, or CERTBOT_EMAIL=…)"
      TLS_MISSING+=("$d"); continue
    fi
    if ! getent ahosts "$d" >/dev/null 2>&1; then
      warn "$d has no DNS record yet — add an A record pointing to this server, then: btnops cert"; TLS_MISSING+=("$d"); continue
    fi
    log "Requesting a Let's Encrypt certificate for $d"
    certbot certonly --webroot -w "$ACME_ROOT" -d "$d" --non-interactive --agree-tos -m "$CERTBOT_EMAIL" \
            --deploy-hook "systemctl reload nginx" \
      || { warn "certbot failed for $d (DNS must point here and port 80 must be reachable) — then: btnops cert"; TLS_MISSING+=("$d"); }
  done
  write_sites
  ((${#TLS_MISSING[@]})) || ok "TLS in place (renewed automatically by certbot.timer)"
elif ((DO_CERTBOT)); then
  warn "certbot is not installed — no TLS yet (apt-get install certbot, then: btnops cert)"
fi

# ── 9b. nightly encrypted database backup (btn-ops-backup.timer, 01:30 Africa/Kigali) ──
if ((DO_BACKEND)); then
  if ! command -v gpg >/dev/null && ((INSTALL_PKGS)); then apt_install gnupg; fi
  if ! command -v gpg >/dev/null; then
    warn "gpg is not installed — nightly backups NOT enabled (apt-get install gnupg, then rerun deploy)"
  else
    log "Enabling nightly encrypted backups"
    install -d -m 750 -o "$RUN_USER" -g "$RUN_USER" /var/backups/btn-ops
    chmod 755 "$APP_DIR"/deploy/*.sh
    if [[ ! -s "$CONF_DIR/backup.pass" ]]; then
      head -c 48 /dev/urandom | base64 > "$CONF_DIR/backup.pass"
      warn "new backup passphrase in $CONF_DIR/backup.pass — keep a copy OFF this server (sudo cat $CONF_DIR/backup.pass); without it no backup can be restored"
    fi
    chown "$RUN_USER:$RUN_USER" "$CONF_DIR/backup.pass"; chmod 400 "$CONF_DIR/backup.pass"
    if [[ ! -f "$CONF_DIR/backup.env" ]]; then
      printf '%s\n' "# written by deploy/deploy.sh — see deploy/backup.env.example for every option (off-site copy, retention…)" \
        "DATABASE_URL='$DATABASE_URL'" "BACKUP_DIR=/var/backups/btn-ops" "BACKUP_KEEP_DAILY=14" "BACKUP_KEEP_WEEKLY=8" \
        "BACKUP_KEEP_MONTHLY=12" "BACKUP_PASSPHRASE_FILE=$CONF_DIR/backup.pass" > "$CONF_DIR/backup.env"
    else  # keep the database in step with the app
      v="${DATABASE_URL//&/\\&}"; sed -i "s|^DATABASE_URL=.*|DATABASE_URL='${v//|/\\|}'|" "$CONF_DIR/backup.env"
    fi
    chown root:"$RUN_USER" "$CONF_DIR/backup.env"; chmod 640 "$CONF_DIR/backup.env"
    for u in btn-ops-backup.service btn-ops-backup.timer; do
      sed -e "s|/opt/btn-ops|$APP_DIR|g" -e "s|^User=.*|User=$RUN_USER|" -e "s|^Group=.*|Group=$RUN_USER|" "$APP_DIR/deploy/$u" > "/etc/systemd/system/$u"
    done
    systemctl daemon-reload
    systemctl enable --now btn-ops-backup.timer >/dev/null 2>&1
    ok "backups: $(systemctl list-timers btn-ops-backup.timer --no-legend 2>/dev/null | awk '{print "next run "$1" "$2" "$3}') (run one now: btnops backup)"
  fi
fi

# ── 10. management CLI ──────────────────────────────────────────────────────
install -m 755 "$APP_DIR/deploy/btnops" /usr/local/bin/btnops
printf 'APP_DIR=%q\nSRC_DIR=%q\nSERVICE=%q\nAPI_PORT=%q\nWEB_DOMAIN=%q\nAPI_DOMAIN=%q\nRUN_USER=%q\nDB_NAME=%q\nDB_USER=%q\nCERTBOT_EMAIL=%q\n' \
  "$APP_DIR" "$SRC_DIR" "$SERVICE" "$API_PORT" "$WEB_DOMAIN" "$API_DOMAIN" "$RUN_USER" "$DB_NAME" "$DB_USER" "$CERTBOT_EMAIL" > "$CONF_FILE"
chmod 644 "$CONF_FILE"

echo; ok "Deploy complete"
scheme() { if [[ -f "/etc/letsencrypt/live/$1/fullchain.pem" ]]; then echo https; else echo http; fi; }
echo "   Web : $(scheme "$WEB_DOMAIN")://$WEB_DOMAIN"
echo "   API : $(scheme "$API_DOMAIN")://$API_DOMAIN/api/health     docs: $(scheme "$API_DOMAIN")://$API_DOMAIN/api/v1/docs"
echo "   Manage: btnops status | btnops logs | btnops restart | btnops update | btnops help"
if ((${#WARNINGS[@]})); then echo; printf '\e[1;33m   Warnings:\e[0m\n'; printf '   - %s\n' "${WARNINGS[@]}"; fi

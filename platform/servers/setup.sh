#!/usr/bin/env bash
# Sets up one AdHunters box for its role (worker, data or standby, from
# /etc/adhunters/role, written at first boot). Run as root over Tailscale:
#
#   sudo ./setup.sh --bin DIR
#
# DIR holds the linux/amd64 binaries to install (see README.md for the build
# line). The script can run again at any time: it changes only what differs,
# never overwrites a secrets file, and never deletes data. Units whose
# settings still hold a placeholder are enabled but not started, and the
# script says what to fill in.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
bin_dir=""
role=""
while [ $# -gt 0 ]; do
    case "$1" in
    --bin) bin_dir=$2; shift 2 ;;
    --role) role=$2; shift 2 ;;
    *) echo "usage: $0 --bin DIR [--role worker|data|standby]" >&2; exit 2 ;;
    esac
done
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
[ -n "$role" ] || role=$(cat /etc/adhunters/role)
case "$role" in worker | data | standby) ;; *) echo "unknown role '$role'" >&2; exit 1 ;; esac
[ -n "$bin_dir" ] && [ -d "$bin_dir" ] || { echo "--bin DIR is required" >&2; exit 2; }

# Private addresses (platform/terraform/servers.tf).
worker_ip=10.20.1.10
data_ip=10.20.1.20
standby_ip=10.20.1.30

todo=()
observe_pw=""
say() { echo "== $*"; }

# ---- every box --------------------------------------------------------------

common() {
    say "base: timezone UTC, the tracks user, folders"
    timedatectl set-timezone UTC
    # Hetzner gives root a random password that has expired, and sudo from
    # root (sudo -u postgres, sudo -u raposa) then asks for a new one. Root
    # still can't log in: SSH is off for it and the password stays unknown.
    if LC_ALL=C chage -l root | grep -q 'password must be changed'; then
        chage -d "$(date +%F)" -M -1 root
        say "cleared the expired root password"
    fi
    id tracks >/dev/null 2>&1 || useradd --system --home-dir /var/lib/tracks --shell /usr/sbin/nologin tracks
    install -d -m 0755 /etc/adhunters /opt/adhunters/bin
    # Metrics files that scripts write for Alloy (pgBackRest on the data box).
    install -d -m 0755 /var/lib/adhunters /var/lib/adhunters/textfile
    install -d -m 0750 -o tracks -g tracks /var/lib/tracks /var/lib/tracks/spool
    # Settings every service reads (the units load it before their own file).
    # An empty SENTRY_DSN leaves Sentry off, so it never holds a unit back.
    if [ ! -f /etc/adhunters/observe.env ]; then
        install -m 0600 /dev/null /etc/adhunters/observe.env
        printf '%s\n' "$observe_env" >/etc/adhunters/observe.env
        say "wrote /etc/adhunters/observe.env"
    fi
}

observe_env='# Read by every AdHunters unit before its own settings (kit/errs).
# The DSN of the Sentry project for the Go services; empty leaves Sentry off.
SENTRY_DSN=
SENTRY_ENVIRONMENT=production'

# install_bin NAME: copies NAME from --bin, keeping the previous build as
# NAME.prev for a quick rollback.
install_bin() {
    local name=$1 src="$bin_dir/$1" dst="/opt/adhunters/bin/$1"
    [ -f "$src" ] || { echo "$src is missing" >&2; exit 1; }
    if [ -f "$dst" ] && cmp -s "$src" "$dst"; then return; fi
    [ -f "$dst" ] && cp -p "$dst" "$dst.prev"
    install -m 0755 "$src" "$dst.new" && mv "$dst.new" "$dst"
    say "installed $name $("$dst" version)"
}

install_unit() {
    install -m 0644 "$here/units/$1" "/etc/systemd/system/$1"
}

# env_file NAME CONTENT [GROUP]: writes /etc/adhunters/NAME.env once, readable
# by GROUP (default tracks); after that it is the owner's and is left alone.
env_file() {
    local f="/etc/adhunters/$1.env"
    if [ ! -f "$f" ]; then
        install -m 0640 -o root -g "${3:-tracks}" /dev/null "$f"
        printf '%s\n' "$2" >"$f"
        say "wrote $f"
    fi
}

# start UNIT ENVFILE: enables UNIT, and (re)starts it only when ENVFILE has
# no placeholder left.
start() {
    local unit=$1 f="/etc/adhunters/$2.env"
    systemctl enable "$unit" >/dev/null
    if grep -q 'FILL_ME' "$f"; then
        todo+=("fill in $f, then: systemctl restart $unit")
        return
    fi
    systemctl restart "$unit"
    say "restarted $unit"
}

shipper_env='# tracks-shipper settings. DATABASE_URL comes from the data box setup.
DATABASE_URL=FILL_ME
ARCHIVE=s3://adhunters-raw
S3_ENDPOINT=fsn1.your-objectstorage.com
S3_REGION=fsn1
S3_ACCESS_KEY=FILL_ME
S3_SECRET_KEY=FILL_ME
OPS_ADDR=127.0.0.1:9103'

capture() { # capture INSTANCE WORKERS PORT
    env_file "tracks-capture@$1" "WORKERS=$2
OPS_ADDR=127.0.0.1:$3"
    if [ ! -s /etc/adhunters/tracks-capture/targets.yaml ] || [ ! -s /etc/adhunters/tracks-capture/proxies.env ]; then
        systemctl enable "tracks-capture@$1" >/dev/null
        todo+=("copy targets.yaml and proxies.env into /etc/adhunters/tracks-capture/, then: systemctl restart tracks-capture@$1")
        return
    fi
    start "tracks-capture@$1" "tracks-capture@$1"
}

capture_box() { # capture_box INSTANCE:WORKERS:PORT...
    install_bin tracks-capture
    install_bin tracks-shipper
    install -d -m 0750 -o root -g tracks /etc/adhunters/tracks-capture
    install_unit tracks-capture@.service
    install_unit tracks-shipper.service
    systemctl daemon-reload
    env_file tracks-shipper "$shipper_env"
    start tracks-shipper tracks-shipper
    local spec
    for spec in "$@"; do
        IFS=: read -r inst workers port <<<"$spec"
        capture "$inst" "$workers" "$port"
        # A deploy restarts one instance at a time, so collection never stops.
        sleep 5
    done
}

# ---- raposa (worker box) ----------------------------------------------------

raposa_src="$repo/raposa"

# nodejs: node 22 from NodeSource. The browser runner needs 20 or later, and
# Ubuntu 24.04 ships 18.
nodejs() {
    local major
    major=$(node --version 2>/dev/null | sed -E 's/^v([0-9]+).*/\1/' || true)
    if [ -z "$major" ] || [ "$major" -lt 20 ]; then
        say "node 22"
        curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
        apt-get install -y -q nodejs
    fi
}

# raposa_browser: the runner's code in /opt/adhunters/raposa-browser, its
# packages, and the Chromium build playwright-core names, in the raposa
# user's home (the unit sets HOME=/var/lib/raposa).
raposa_browser() {
    local dst=/opt/adhunters/raposa-browser changed=0 f
    install -d -m 0755 "$dst"
    for f in runner.js keep.js package.json package-lock.json; do
        if ! cmp -s "$raposa_src/browser/$f" "$dst/$f"; then
            install -m 0644 "$raposa_src/browser/$f" "$dst/$f"
            changed=1
        fi
    done
    if [ "$changed" = 1 ] || [ ! -d "$dst/node_modules" ]; then
        say "raposa browser: packages"
        (cd "$dst" && npm ci --omit=dev --no-audit --no-fund)
    fi
    # The libraries Chromium needs (as root), then the build (as raposa).
    # Both do nothing when they are there already.
    (cd "$dst" && npx --no-install playwright-core install-deps chromium >/dev/null)
    (cd "$dst" && sudo -u raposa HOME=/var/lib/raposa npx --no-install playwright-core install chromium)
}

# example NAME: the settings lines of raposa/deploy/NAME.env.example.
example() {
    grep -E '^[A-Z0-9_]+=' "$raposa_src/deploy/$1.env.example" || true
}

raposa_box() {
    [ -d "$raposa_src/deploy" ] || { echo "$raposa_src is missing: run setup.sh from a checkout of the repository" >&2; exit 1; }
    say "raposa: the raposa user, folders"
    id raposa >/dev/null 2>&1 || useradd --system --home-dir /var/lib/raposa --shell /usr/sbin/nologin raposa
    install -d -m 0750 -o raposa -g raposa /var/lib/raposa /var/lib/raposa/keep /var/lib/raposa/files
    install -d -m 0750 -o root -g raposa /etc/adhunters/raposa
    nodejs
    install_bin raposa-engine
    install_bin raposa-web
    raposa_browser
    local u
    for u in raposa-browser.service raposa-engine.service raposa-web.service; do
        install -m 0644 "$raposa_src/deploy/$u" "/etc/systemd/system/$u"
    done
    systemctl daemon-reload

    # Raposa uses the proxy lines and targets capture uses. Copied once;
    # after that they are the owner's.
    local f
    for f in proxies.env targets.yaml; do
        if [ ! -s "/etc/adhunters/raposa/$f" ] && [ -s "/etc/adhunters/tracks-capture/$f" ]; then
            install -m 0640 -o root -g raposa "/etc/adhunters/tracks-capture/$f" "/etc/adhunters/raposa/$f"
            say "copied $f from /etc/adhunters/tracks-capture"
        fi
    done

    env_file raposa-browser "# raposa-browser settings; all optional (raposa/browser/README.md)." raposa
    env_file raposa-engine "$(example raposa-engine)" raposa
    env_file raposa-web "$(example raposa-web)" raposa

    # The runner first: the engine's browser rungs and keeper call it.
    systemctl enable raposa-browser >/dev/null
    systemctl restart raposa-browser
    say "restarted raposa-browser"
    if [ ! -s /etc/adhunters/raposa/proxies.env ] || [ ! -s /etc/adhunters/raposa/targets.yaml ]; then
        systemctl enable raposa-engine >/dev/null
        todo+=("copy proxies.env and targets.yaml into /etc/adhunters/raposa/ (root, group raposa, 0640), then: systemctl restart raposa-engine")
    else
        start raposa-engine raposa-engine
    fi
    start raposa-web raposa-web
}

# ---- data box ---------------------------------------------------------------

postgres() {
    say "postgres 17"
    if [ ! -x /usr/lib/postgresql/17/bin/postgres ]; then
        apt-get install -y -q postgresql-common
        /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y
        apt-get install -y -q postgresql-17
    fi
    local conf=/etc/postgresql/17/main/conf.d/adhunters.conf
    local want
    # Sized for a CX43 (8 vCPU, 16 GB) that also runs the data box's services.
    want="# Written by platform/servers/setup.sh; edit there.
listen_addresses = 'localhost,$data_ip'
max_connections = 80
ssl = on
password_encryption = scram-sha-256
timezone = 'UTC'
log_timezone = 'UTC'
shared_buffers = 4GB
effective_cache_size = 10GB
work_mem = 32MB
maintenance_work_mem = 1GB
random_page_cost = 1.1
effective_io_concurrency = 200
wal_compression = zstd
max_wal_size = 8GB
checkpoint_timeout = 15min
log_min_duration_statement = 2s
log_lock_waits = on
shared_preload_libraries = 'pg_stat_statements'"
    local restart=0
    if [ "$(cat "$conf" 2>/dev/null)" != "$want" ]; then
        printf '%s\n' "$want" >"$conf"
        restart=1
    fi
    local hba=/etc/postgresql/17/main/pg_hba.conf
    local line
    for line in "hostssl adhunters tracks_shipper $worker_ip/32 scram-sha-256" \
        "hostssl adhunters tracks_shipper $standby_ip/32 scram-sha-256" \
        "hostssl adhunters raposa $worker_ip/32 scram-sha-256"; do
        grep -qxF "$line" "$hba" || { echo "$line" >>"$hba"; restart=1; }
    done
    systemctl enable postgresql >/dev/null
    if [ "$restart" = 1 ]; then systemctl restart postgresql; else systemctl start postgresql; fi
}

psql_su() { sudo -u postgres psql -v ON_ERROR_STOP=1 -qAt "$@"; }

# login ROLE: creates the login with a new random password the first time and
# prints the password once; later runs leave it alone.
login() {
    if [ "$(psql_su -c "SELECT 1 FROM pg_roles WHERE rolname = '$1'")" != 1 ]; then
        local pw
        pw=$(openssl rand -hex 24)
        psql_su -c "CREATE ROLE $1 LOGIN PASSWORD '$pw'"
        echo "$pw"
    fi
}

data_box() {
    postgres
    install_bin tracks-loader
    install_unit tracks-loader.service
    systemctl daemon-reload

    say "database adhunters and the tracks logins"
    [ "$(psql_su -c "SELECT 1 FROM pg_database WHERE datname = 'adhunters'")" = 1 ] ||
        psql_su -c "CREATE DATABASE adhunters"
    psql_su -d adhunters -c "ALTER DATABASE adhunters SET timezone = 'UTC'"
    psql_su -d adhunters -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"
    psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tracks_api_read') THEN CREATE ROLE tracks_api_read NOLOGIN; END IF; END \$\$"

    local loader_pw shipper_pw
    loader_pw=$(login tracks_loader)
    shipper_pw=$(login tracks_shipper)
    # Alloy's read-only login for Postgres metrics.
    observe_pw=$(login observe)
    psql_su -c "GRANT pg_monitor TO observe"
    # The loader owns the tracks schemas and runs their migrations.
    psql_su -d adhunters -c "GRANT CREATE ON DATABASE adhunters TO tracks_loader"
    psql_su -d adhunters -c "REVOKE CREATE ON SCHEMA public FROM PUBLIC"

    if [ -n "$loader_pw" ]; then
        env_file tracks-loader "# tracks-loader settings.
DATABASE_URL=postgres://tracks_loader:$loader_pw@localhost:5432/adhunters?sslmode=require
ARCHIVE=s3://adhunters-raw
S3_ENDPOINT=fsn1.your-objectstorage.com
S3_REGION=fsn1
S3_ACCESS_KEY=FILL_ME
S3_SECRET_KEY=FILL_ME
OPS_ADDR=127.0.0.1:9104"
    else
        env_file tracks-loader "DATABASE_URL=FILL_ME
ARCHIVE=s3://adhunters-raw
S3_ENDPOINT=fsn1.your-objectstorage.com
S3_REGION=fsn1
S3_ACCESS_KEY=FILL_ME
S3_SECRET_KEY=FILL_ME
OPS_ADDR=127.0.0.1:9104"
    fi
    if [ -n "$shipper_pw" ]; then
        todo+=("put this line in /etc/adhunters/tracks-shipper.env on the worker and standby boxes (shown once):
    DATABASE_URL=postgres://tracks_shipper:$shipper_pw@$data_ip:5432/adhunters?sslmode=require")
    fi

    # The loader's start runs its migrations, which also grant the shipper
    # and tracks_api_read their rights.
    start tracks-loader tracks-loader

    say "the raposa login"
    # raposa-engine owns the raposa and raposa_api schemas and runs their
    # migrations from the worker box; it reads Tracks through tracks_api.
    # raposa_api_read must exist before its first migration, which grants it.
    psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'raposa_api_read') THEN CREATE ROLE raposa_api_read NOLOGIN; END IF; END \$\$"
    local raposa_pw
    raposa_pw=$(login raposa)
    psql_su -d adhunters -c "GRANT CREATE ON DATABASE adhunters TO raposa"
    psql_su -d adhunters -c "GRANT tracks_api_read TO raposa"
    if [ -n "$raposa_pw" ]; then
        todo+=("put this line in /etc/adhunters/raposa-engine.env and raposa-web.env on the worker box (shown once):
    DATABASE_URL=postgres://raposa:$raposa_pw@$data_ip:5432/adhunters?sslmode=require")
    fi

    # The 08:00 digest and the Sentry relay (platform/observe).
    install_bin observe-bot
    install_unit observe-bot.service
    systemctl daemon-reload
    env_file observe-bot "$observe_bot_env"
    start observe-bot observe-bot
}

observe_bot_env='# observe-bot settings (see platform/observe/README.md).
TELEGRAM_BOT_TOKEN=FILL_ME
TELEGRAM_CHAT_ID=FILL_ME
# The stack'"'"'s Prometheus URL followed by /api/prom, its user number, and a
# token with metrics:read.
GRAFANA_QUERY_URL=FILL_ME
GRAFANA_QUERY_USER=FILL_ME
GRAFANA_QUERY_TOKEN=FILL_ME
# Sentry, read-only (an internal integration token with Issue & Event: Read).
# Leave SENTRY_API_TOKEN empty to keep the relay off.
SENTRY_URL=https://sentry.io
SENTRY_ORG=FILL_ME
SENTRY_PROJECT=adhunters-go
SENTRY_API_TOKEN=FILL_ME
OPS_ADDR=127.0.0.1:9107'

# ---- backups (data box) -------------------------------------------------------

pgbackrest_conf='# pgBackRest (root:postgres 0640). Written once by setup.sh; the keys are
# for the object storage bucket adhunters-backups.
[global]
repo1-type=s3
repo1-s3-endpoint=fsn1.your-objectstorage.com
repo1-s3-region=fsn1
repo1-s3-bucket=adhunters-backups
repo1-s3-uri-style=path
repo1-s3-key=FILL_ME
repo1-s3-key-secret=FILL_ME
repo1-path=/pgbackrest
repo1-retention-full=4
repo1-bundle=y
compress-type=zst
process-max=2
start-fast=y
archive-async=y
spool-path=/var/spool/pgbackrest
log-level-console=warn

[adhunters]
pg1-path=/var/lib/postgresql/17/main'

# backups: WAL archiving every 60 s and daily backups to object storage. WAL
# archiving is only switched on once the keys are in: an archive command that
# keeps failing makes Postgres keep every WAL file and fills the disk.
backups() {
    say "pgbackrest"
    command -v pgbackrest >/dev/null || apt-get install -y -q pgbackrest
    command -v jq >/dev/null || apt-get install -y -q jq
    install -d -m 0750 -o postgres -g postgres /var/spool/pgbackrest /var/log/pgbackrest
    local conf=/etc/pgbackrest/pgbackrest.conf
    install -d -m 0755 /etc/pgbackrest
    if [ ! -f "$conf" ] || ! grep -q '^\[adhunters\]' "$conf"; then
        install -m 0640 -o root -g postgres /dev/null "$conf"
        printf '%s\n' "$pgbackrest_conf" >"$conf"
        say "wrote $conf"
    fi
    install -m 0755 "$here/pgbackrest-metrics.sh" /opt/adhunters/bin/pgbackrest-metrics
    install_unit pgbackrest-backup@.service
    install_unit pgbackrest-full.timer
    install_unit pgbackrest-diff.timer
    systemctl daemon-reload

    local archive=/etc/postgresql/17/main/conf.d/archive.conf
    if grep -q FILL_ME "$conf"; then
        if [ -f "$archive" ]; then rm -f "$archive" && systemctl restart postgresql; fi
        systemctl disable --now pgbackrest-full.timer pgbackrest-diff.timer >/dev/null 2>&1 || true
        todo+=("fill in the object storage keys in $conf, then run setup.sh again to switch on WAL archiving and backups")
        return
    fi
    if put "$archive" "# Written by platform/servers/setup.sh; edit there.
archive_mode = on
archive_command = 'pgbackrest --stanza=adhunters archive-push %p'
archive_timeout = 60"; then
        systemctl restart postgresql
        say "WAL archiving on"
    fi
    sudo -u postgres pgbackrest --stanza=adhunters stanza-create
    sudo -u postgres pgbackrest --stanza=adhunters check
    systemctl enable --now pgbackrest-full.timer pgbackrest-diff.timer >/dev/null
    # No full backup yet: take one now, in the background.
    if ! sudo -u postgres pgbackrest --stanza=adhunters --output=json info | jq -e '.[0].backup | length > 0' >/dev/null; then
        systemctl start --no-block pgbackrest-backup@full.service
        say "first full backup started"
    fi
    /opt/adhunters/bin/pgbackrest-metrics
}

# ---- Grafana Alloy (every box) ----------------------------------------------

# Every service's /metrics port (kit/ops, OPS_ADDR). A unit that is enabled on
# this box is scraped; the others are left out so they never read as down.
#   unit                    port  service         instance
ops_ports='tracks-capture@a        9101  tracks-capture  a
tracks-capture@b        9102  tracks-capture  b
tracks-capture@standby  9101  tracks-capture  standby
tracks-shipper          9103  tracks-shipper  -
tracks-loader           9104  tracks-loader   -
raposa-engine           9105  raposa-engine   -
raposa-web              9106  raposa-web      -
observe-bot             9107  observe-bot     -'

alloy_env='# Grafana Alloy settings (root only); see platform/observe/README.md.
GRAFANA_METRICS_URL=FILL_ME
GRAFANA_METRICS_USER=FILL_ME
GRAFANA_LOGS_URL=FILL_ME
GRAFANA_LOGS_USER=FILL_ME
GRAFANA_CLOUD_TOKEN=FILL_ME'

# services_alloy: the scrape block for the units enabled on this box.
services_alloy() {
    local unit port svc inst targets=""
    while read -r unit port svc inst; do
        systemctl is-enabled --quiet "$unit" 2>/dev/null || continue
        [ "$inst" = - ] && inst=$(hostname)
        targets+="    {\"__address__\" = \"127.0.0.1:$port\", \"service\" = \"$svc\", \"instance\" = \"$inst\"},
"
    done <<<"$ops_ports"
    cat <<ALLOY
// Written by platform/servers/setup.sh from the units enabled on this box.
prometheus.scrape "services" {
  job_name        = "adhunters"
  scrape_interval = "60s"
  targets         = [
${targets}  ]
  forward_to = [prometheus.remote_write.cloud.receiver]
}
ALLOY
}

# put FILE CONTENT: writes FILE when its content differs; says whether it did.
put() {
    if [ "$(cat "$1" 2>/dev/null)" != "$2" ]; then
        printf '%s\n' "$2" >"$1"
        return 0
    fi
    return 1
}

alloy_agent() {
    say "grafana alloy"
    if ! command -v alloy >/dev/null; then
        install -d -m 0755 /etc/apt/keyrings
        curl -fsSL https://apt.grafana.com/gpg.key | gpg --dearmor --yes -o /etc/apt/keyrings/grafana.gpg
        echo "deb [signed-by=/etc/apt/keyrings/grafana.gpg] https://apt.grafana.com stable main" >/etc/apt/sources.list.d/grafana.list
        apt-get update -q
        apt-get install -y -q alloy
    fi
    # Journal access for the logs.
    usermod -aG systemd-journal alloy

    local dir=/etc/alloy/adhunters changed=0
    install -d -m 0755 "$dir"
    put "$dir/common.alloy" "$(cat "$here/../observe/alloy/common.alloy")" && changed=1
    put "$dir/services.alloy" "$(services_alloy)" && changed=1
    if [ "$role" = data ]; then
        put "$dir/postgres.alloy" "$(cat "$here/../observe/alloy/postgres.alloy")" && changed=1
    fi
    # The package's own settings file: read the whole folder, keep the UI on
    # localhost, no usage reports.
    put /etc/default/alloy "# Written by platform/servers/setup.sh; edit there.
CONFIG_FILE=$dir
CUSTOM_ARGS=\"--server.http.listen-addr=127.0.0.1:12345 --disable-reporting\"
RESTART_ON_UPGRADE=true" && changed=1
    install -d -m 0755 /etc/systemd/system/alloy.service.d
    put /etc/systemd/system/alloy.service.d/adhunters.conf "[Service]
EnvironmentFile=/etc/adhunters/alloy.env
Environment=ADHUNTERS_BOX=$role" && changed=1
    systemctl daemon-reload

    if [ ! -f /etc/adhunters/alloy.env ]; then
        install -m 0600 /dev/null /etc/adhunters/alloy.env
        printf '%s\n' "$alloy_env" >/etc/adhunters/alloy.env
        [ "$role" = data ] && echo "POSTGRES_MONITOR_URL=FILL_ME" >>/etc/adhunters/alloy.env
        say "wrote /etc/adhunters/alloy.env"
    fi
    if [ "$role" = data ] && [ -n "$observe_pw" ]; then
        sed -i "s|^POSTGRES_MONITOR_URL=FILL_ME\$|POSTGRES_MONITOR_URL=postgres://observe:$observe_pw@localhost:5432/adhunters?sslmode=require|" /etc/adhunters/alloy.env
    fi

    systemctl enable alloy >/dev/null
    if grep -q FILL_ME /etc/adhunters/alloy.env; then
        systemctl stop alloy 2>/dev/null || true
        todo+=("fill in /etc/adhunters/alloy.env (Grafana Cloud), then: systemctl restart alloy")
    elif [ "$changed" = 1 ] || ! systemctl is-active --quiet alloy; then
        systemctl restart alloy
        say "restarted alloy"
    fi
}

# ---- roles ------------------------------------------------------------------

common
case "$role" in
worker)
    capture_box a:4:9101 b:4:9102
    raposa_box
    ;;
standby) capture_box standby:1:9101 ;;
data) data_box && backups ;;
esac
alloy_agent

say "done: $role"
if [ ${#todo[@]} -gt 0 ]; then
    echo
    echo "Still to do on this box:"
    for t in "${todo[@]}"; do echo "  - $t"; done
fi

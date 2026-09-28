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
say() { echo "== $*"; }

# ---- every box --------------------------------------------------------------

common() {
    say "base: timezone UTC, the tracks user, folders"
    timedatectl set-timezone UTC
    id tracks >/dev/null 2>&1 || useradd --system --home-dir /var/lib/tracks --shell /usr/sbin/nologin tracks
    install -d -m 0755 /etc/adhunters /opt/adhunters/bin
    install -d -m 0750 -o tracks -g tracks /var/lib/tracks /var/lib/tracks/spool
}

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

# env_file NAME CONTENT: writes /etc/adhunters/NAME.env once; after that it
# is the owner's and is left alone.
env_file() {
    local f="/etc/adhunters/$1.env"
    if [ ! -f "$f" ]; then
        install -m 0640 -o root -g tracks /dev/null "$f"
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
        "hostssl adhunters tracks_shipper $standby_ip/32 scram-sha-256"; do
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
}

# ---- roles ------------------------------------------------------------------

common
case "$role" in
worker) capture_box a:4:9101 b:4:9102 ;;
standby) capture_box standby:1:9101 ;;
data) data_box ;;
esac

say "done: $role"
if [ ${#todo[@]} -gt 0 ]; then
    echo
    echo "Still to do on this box:"
    for t in "${todo[@]}"; do echo "  - $t"; done
fi

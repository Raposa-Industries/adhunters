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
    private_net
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

# private_net: switches on the Hetzner private network card when Ubuntu left
# it down (it happens when Hetzner attaches it without a netplan entry): the
# worker and standby reach Postgres at 10.20.1.20 through it.
private_net() {
    local nic=enp7s0 f=/etc/netplan/60-private.yaml
    ip link show "$nic" >/dev/null 2>&1 || return 0
    ip -4 addr show "$nic" | grep -q 'inet 10\.20\.' && return 0
    say "private network: dhcp on $nic"
    install -m 0600 /dev/null "$f"
    printf 'network:\n  version: 2\n  ethernets:\n    %s:\n      dhcp4: true\n' "$nic" >"$f"
    netplan apply
    sleep 3
    ip -4 addr show "$nic" | grep -q 'inet 10\.20\.' ||
        todo+=("$nic has no 10.20.1.x address after netplan apply: check the box's network in the Hetzner Console")
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
    # Without its lines an instance cannot run, so it stays disabled: Alloy
    # watches only enabled units, and one that cannot run would read as down.
    # The standby's stays so until it has lines of its own (SWITCH-OVER.md).
    if [ ! -s /etc/adhunters/tracks-capture/targets.yaml ] || [ ! -s /etc/adhunters/tracks-capture/proxies.env ]; then
        systemctl disable --quiet "tracks-capture@$1"
        todo+=("copy targets.yaml and proxies.env into /etc/adhunters/tracks-capture/, then run this setup again: it starts tracks-capture@$1")
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

# ---- tracks-walker (worker box) ---------------------------------------------

# walker_box: tracks-walker walks running ads' links to their landing pages
# (tracks/README.md), through capture's proxy lines file. Its DATABASE_URL
# comes from the data box setup.
walker_box() {
    install_bin tracks-walker
    install_unit tracks-walker.service
    systemctl daemon-reload
    install -d -m 0750 -o tracks -g tracks /var/lib/tracks/walk-spool
    env_file tracks-walker '# tracks-walker settings. DATABASE_URL comes from the data box setup.
DATABASE_URL=FILL_ME
ARCHIVE=s3://adhunters-raw
S3_ENDPOINT=fsn1.your-objectstorage.com
S3_REGION=fsn1
S3_ACCESS_KEY=FILL_ME
S3_SECRET_KEY=FILL_ME
WORKERS=2
OPS_ADDR=127.0.0.1:9119'
    if [ ! -s /etc/adhunters/tracks-capture/proxies.env ]; then
        systemctl enable tracks-walker >/dev/null
        todo+=("copy proxies.env into /etc/adhunters/tracks-capture/, then: systemctl restart tracks-walker")
        return
    fi
    start tracks-walker tracks-walker
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

# ---- Create on the data box ------------------------------------------------

create_src="$repo/create"

# create_web: the team's sign-in in front of every app (create/README.md).
# It starts once SIGNIN_USER and SIGNIN_PASSWORD_HASH are filled in. People
# reach it through the Cloudflare tunnel (platform/OPERATIONS.md).
create_web() {
    [ -d "$create_src/deploy" ] || { echo "$create_src is missing: run setup.sh from a checkout of the repository" >&2; exit 1; }
    install_bin create-web
    install -m 0644 "$create_src/deploy/create-web.service" /etc/systemd/system/create-web.service
    systemctl daemon-reload
    env_file create-web "$(grep -E '^[A-Z0-9_]+=' "$create_src/deploy/create-web.env.example")"
    start create-web create-web
}

# ---- Funnels on the data box -----------------------------------------------

funnels_src="$repo/funnels"

# funnels_example NAME: the settings lines of funnels/deploy/NAME.env.example.
funnels_example() {
    grep -E '^[A-Z0-9_]+=' "$funnels_src/deploy/$1.env.example" || true
}

# funnels_box: the edge (hosted landing sites, /ah.js, /e) and the loader
# (funnels/README.md). Visitors reach the edge through the Cloudflare tunnel
# (platform/OPERATIONS.md). The edge needs no database, so it starts first.
funnels_box() {
    [ -d "$funnels_src/deploy" ] || { echo "$funnels_src is missing: run setup.sh from a checkout of the repository" >&2; exit 1; }
    say "funnels: the funnels user, folders, the funnels login"
    id funnels >/dev/null 2>&1 || useradd --system --home-dir /var/lib/funnels --shell /usr/sbin/nologin funnels
    install -d -m 0750 -o funnels -g funnels /var/lib/funnels /var/lib/funnels/spool /var/lib/funnels/sites
    install_bin funnels-edge
    install_bin funnels-loader
    install_bin funnels-web
    for u in funnels-edge.service funnels-loader.service funnels-web.service; do
        install -m 0644 "$funnels_src/deploy/$u" "/etc/systemd/system/$u"
    done
    systemctl daemon-reload

    # The key that hashes visitors' addresses: made once, never shown.
    env_file funnels-edge "$(funnels_example funnels-edge | sed "s|^FUNNELS_IP_KEY=FILL_ME\$|FUNNELS_IP_KEY=$(openssl rand -hex 24)|")" funnels
    start funnels-edge funnels-edge

    # funnels-loader owns the funnels, funnels_draft and funnels_api schemas
    # and runs their migrations; funnels_api_read and funnels_web_read must
    # exist before the migrations that grant them.
    local r
    for r in funnels_api_read funnels_web_read; do
        psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$r') THEN CREATE ROLE $r NOLOGIN; END IF; END \$\$"
    done
    local pw web_pw k v f=/etc/adhunters/funnels-loader.env
    pw=$(login funnels)
    web_pw=$(login funnels_web)
    psql_su -d adhunters -c "GRANT funnels_web_read TO funnels_web"
    psql_su -d adhunters -c "GRANT CREATE ON DATABASE adhunters TO funnels"
    env_file funnels-loader "$(funnels_example funnels-loader)" funnels
    if [ -n "$pw" ]; then
        sed -i "s|^DATABASE_URL=FILL_ME\$|DATABASE_URL=postgres://funnels:$pw@localhost:5432/adhunters?sslmode=require|" "$f"
    fi
    # The same object storage keys as Tracks' loader.
    for k in S3_ACCESS_KEY S3_SECRET_KEY; do
        v=$(grep -E "^$k=" /etc/adhunters/tracks-loader.env 2>/dev/null | cut -d= -f2- || true)
        if [ -n "$v" ] && [ "$v" != FILL_ME ]; then
            sed -i "s|^$k=FILL_ME\$|$k=$v|" "$f"
        fi
    done
    start funnels-loader funnels-loader

    # funnels-web: the pages under /funnels/, reading through funnels_web.
    env_file funnels-web "$(funnels_example funnels-web)" funnels
    if [ -n "$web_pw" ]; then
        sed -i "s|^DATABASE_URL=postgres://funnels_web:FILL_ME@|DATABASE_URL=postgres://funnels_web:$web_pw@|" /etc/adhunters/funnels-web.env
    fi
    start funnels-web funnels-web
}

# ---- Intel on the data box -------------------------------------------------

intel_src="$repo/intel"

# intel_box: Intel's three services (intel/README.md), next to its database.
# intel-numbers owns the intel and intel_api schemas and migrates them before
# each start; it reads Launch's moves through launch_api_read. intel-collect
# waits for its Taboola and RedTrack keys; intel-web listens on localhost for
# the Cloudflare tunnel.
intel_box() {
    [ -d "$intel_src/deploy" ] || { echo "$intel_src is missing: run setup.sh from a checkout of the repository" >&2; exit 1; }
    say "the intel login"
    local r
    for r in intel_api_read launch_api_read; do
        psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$r') THEN CREATE ROLE $r NOLOGIN; END IF; END \$\$"
    done
    local intel_pw url
    intel_pw=$(login intel)
    psql_su -d adhunters -c "GRANT CREATE ON DATABASE adhunters TO intel"
    psql_su -d adhunters -c "GRANT launch_api_read TO intel"
    # The logins added on Launch's Contas page and its account proxies, sealed
    # (decision 0028). Launch's migration grants them when the intel login is
    # already there.
    psql_su -d adhunters -c "DO \$\$ BEGIN IF to_regclass('launch_api.taboola_login_v1') IS NOT NULL THEN GRANT SELECT ON launch_api.taboola_login_v1 TO intel; END IF; END \$\$"
    psql_su -d adhunters -c "DO \$\$ BEGIN IF to_regclass('launch_api.taboola_account_proxy_v1') IS NOT NULL THEN GRANT SELECT ON launch_api.taboola_account_proxy_v1 TO intel; END IF; END \$\$"
    url=FILL_ME
    [ -n "$intel_pw" ] && url="postgres://intel:$intel_pw@localhost:5432/adhunters?sslmode=require"

    local b
    for b in intel-collect intel-numbers intel-web; do
        install_bin "$b"
        install -m 0644 "$intel_src/deploy/$b.service" "/etc/systemd/system/$b.service"
        # The login's password goes in once, when it is made; the settings
        # file is the owner's after that.
        env_file "$b" "$(grep -E '^[A-Z0-9_]+=' "$intel_src/deploy/$b.env.example" |
            sed -e "s|^DATABASE_URL=.*|DATABASE_URL=$url|")"
    done
    systemctl daemon-reload
    start intel-numbers intel-numbers
    start intel-web intel-web
    start intel-collect intel-collect
}

# ---- the library on the data box -------------------------------------------

library_src="$repo/library"

# library_box: the library Create and Launch share (library/README.md). It
# owns the library and library_api schemas and runs their migrations; the
# apps read library_api through library_api_read. Its Drive sync stays off
# until the Google client is filled in and someone runs drive-login.
library_box() {
    [ -d "$library_src/deploy" ] || { echo "$library_src is missing: run setup.sh from a checkout of the repository" >&2; exit 1; }
    say "the library login"
    psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'library_api_read') THEN CREATE ROLE library_api_read NOLOGIN; END IF; END \$\$"
    local library_pw
    library_pw=$(login library)
    psql_su -d adhunters -c "GRANT CREATE ON DATABASE adhunters TO library"
    install_bin library
    install -m 0644 "$library_src/deploy/library.service" /etc/systemd/system/library.service
    systemctl daemon-reload
    local example
    example=$(grep -E '^[A-Z0-9_]+=' "$library_src/deploy/library.env.example")
    if [ -n "$library_pw" ]; then
        example=$(printf '%s\n' "$example" | sed "s|^DATABASE_URL=FILL_ME\$|DATABASE_URL=postgres://library:$library_pw@localhost:5432/adhunters?sslmode=require|")
    fi
    env_file library "$example"
    start library library
    if grep -q '^LIBRARY_GOOGLE_CLIENT_ID=$' /etc/adhunters/library.env; then
        todo+=("to copy the library to Google Drive: put the Google client id and secret in /etc/adhunters/library.env, systemctl restart library, then run: sudo /opt/adhunters/bin/library drive-login")
    fi
}

# ---- Create on the data box ---------------------------------------------------

# create_box: AdHunters Create (create/README.md). It owns the create_app and
# create_api schemas ("create" is a reserved word in SQL) and runs their
# migrations; Desk calls create_api through create_api_read. It saves into
# the library over HTTP. For the Spy → Create entry it reads tracks_api
# (creative_v1 for the image, ad_v1 for the latest headline) and spy_api
# (creative_class_v1 for the vertical). spy_box makes spy_api_read too; it is
# made here as well because create_box runs first on a new box.
create_box() {
    say "the create_app login"
    psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'create_api_read') THEN CREATE ROLE create_api_read NOLOGIN; END IF; END \$\$"
    local create_pw
    create_pw=$(login create_app)
    psql_su -d adhunters -c "GRANT CREATE ON DATABASE adhunters TO create_app"
    local r
    for r in spy_api_read launch_api_read; do
        psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$r') THEN CREATE ROLE $r NOLOGIN; END IF; END \$\$"
    done
    # launch_api_read: Create's Biblioteca shows how many Launch ads use each picture.
    psql_su -d adhunters -c "GRANT tracks_api_read, spy_api_read, launch_api_read TO create_app"
    install_bin create
    install -m 0644 "$create_src/deploy/create.service" /etc/systemd/system/create.service
    systemctl daemon-reload
    local example
    example=$(grep -E '^[A-Z0-9_]+=' "$create_src/deploy/create.env.example")
    if [ -n "$create_pw" ]; then
        example=$(printf '%s\n' "$example" | sed "s|^DATABASE_URL=FILL_ME\$|DATABASE_URL=postgres://create_app:$create_pw@localhost:5432/adhunters?sslmode=require|")
    fi
    env_file create "$example"
    start create create
    if grep -q '^OPENAI_API_KEY=$' /etc/adhunters/create.env; then
        todo+=("to make options in Create: put the OpenAI key in /etc/adhunters/create.env (OPENAI_API_KEY=), then systemctl restart create")
    fi
}

# ---- Desk on the data box ---------------------------------------------------

desk_src="$repo/desk"

# desk_box: AdHunters Desk (desk/README.md). The desk login owns the desk and
# desk_api schemas; both units run the migrations before they start. Desk is
# off until its Claude key is in: desk-agent runs but takes no work.
desk_box() {
    [ -d "$desk_src/deploy" ] || { echo "$desk_src is missing: run setup.sh from a checkout of the repository" >&2; exit 1; }
    say "the desk login"
    # desk_api_read must exist before Desk's first migration, which grants it.
    psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'desk_api_read') THEN CREATE ROLE desk_api_read NOLOGIN; END IF; END \$\$"
    local desk_pw
    desk_pw=$(login desk)
    psql_su -d adhunters -c "GRANT CREATE ON DATABASE adhunters TO desk"
    # Desk reads and calls each app in contract/actions through that app's
    # <app>_api_read role. A role made here before its app is set up gets its
    # rights from the app's first migration.
    local f r
    for f in "$repo"/contract/actions/*.json; do
        r="$(basename "$f" .json)_api_read"
        psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$r') THEN CREATE ROLE $r NOLOGIN; END IF; END \$\$"
        psql_su -d adhunters -c "GRANT $r TO desk"
    done
    install_bin desk-agent
    install_bin desk-web
    local u example
    for u in desk-agent desk-web; do
        install -m 0644 "$desk_src/deploy/$u.service" "/etc/systemd/system/$u.service"
    done
    systemctl daemon-reload
    for u in desk-agent desk-web; do
        example=$(grep -E '^[A-Z0-9_]+=' "$desk_src/deploy/$u.env.example")
        if [ -n "$desk_pw" ]; then
            example=$(printf '%s\n' "$example" | sed "s|^DATABASE_URL=FILL_ME\$|DATABASE_URL=postgres://desk:$desk_pw@localhost:5432/adhunters?sslmode=require|")
        fi
        env_file "$u" "$example"
    done
    # The first example had FILL_ME for the key, which kept desk-agent
    # stopped, so it read as down; an empty key now means Desk is off.
    sed -i 's/^ANTHROPIC_API_KEY=FILL_ME$/ANTHROPIC_API_KEY=/' /etc/adhunters/desk-agent.env
    # Settings files written before Desk checked Access's token lack its two
    # lines; empty, every page says to sign in.
    local k
    for k in ACCESS_TEAM ACCESS_AUD; do
        grep -q "^$k=" /etc/adhunters/desk-web.env || echo "$k=" >>/etc/adhunters/desk-web.env
    done
    start desk-web desk-web
    start desk-agent desk-agent
    if grep -q '^ANTHROPIC_API_KEY=$' /etc/adhunters/desk-agent.env || grep -q '^ACCESS_AUD=$' /etc/adhunters/desk-web.env; then
        todo+=("Desk is off: to turn it on, put the Claude key in /etc/adhunters/desk-agent.env (ANTHROPIC_API_KEY=) and the Access application's ACCESS_TEAM and ACCESS_AUD in /etc/adhunters/desk-web.env, then systemctl restart desk-agent desk-web, and add the ^/desk tunnel route (platform/OPERATIONS.md)")
    fi
}

# ---- Spy on the data box ---------------------------------------------------

spy_src="$repo/spy"

# spy_box: spy-numbers (Spy's numbers, grouping and classifier; it owns the
# spy schemas and runs their migrations before each start) and spy-web (the
# pages, read only). Both reach Postgres on this box. spy_api_read must
# exist before spy-numbers' first migration, which grants it.
spy_box() {
    [ -d "$spy_src/deploy" ] || { echo "$spy_src is missing: run setup.sh from a checkout of the repository" >&2; exit 1; }
    say "spy: the spy user, logins, units"
    id spy >/dev/null 2>&1 || useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin spy
    install_bin spy-numbers
    install_bin spy-web
    local u
    for u in spy-numbers.service spy-web.service; do
        install -m 0644 "$spy_src/deploy/$u" "/etc/systemd/system/$u"
    done
    systemctl daemon-reload

    psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'spy_api_read') THEN CREATE ROLE spy_api_read NOLOGIN; END IF; END \$\$"
    local spy_pw web_pw
    spy_pw=$(login spy)
    web_pw=$(login spy_web)
    psql_su -d adhunters -c "GRANT CREATE ON DATABASE adhunters TO spy"
    psql_su -d adhunters -c "GRANT tracks_api_read, raposa_api_read TO spy"
    psql_su -d adhunters -c "GRANT spy_api_read, tracks_api_read, raposa_api_read TO spy_web"

    env_file spy-numbers "# spy-numbers settings (spy/README.md). OLD_DATABASE_URL is only for
# spy-numbers import-old: a read-only login on the collector's database.
DATABASE_URL=postgres://spy:${spy_pw:-FILL_ME}@localhost:5432/adhunters?sslmode=require
OLD_DATABASE_URL=
OPS_ADDR=127.0.0.1:9122
# Watch notices go to the ops group: the bot token and the group's chat id.
TELEGRAM_BOT_TOKEN=
OPS_TELEGRAM_CHAT_ID=
SPY_BASE_URL=https://hunt-teste.fyi" spy
    env_file spy-web "# spy-web settings (spy/README.md). ACCESS_TEAM is the team's address
# (https://<team>.cloudflareaccess.com), ACCESS_AUD the Access application's AUD tag.
DATABASE_URL=postgres://spy_web:${web_pw:-FILL_ME}@localhost:5432/adhunters?sslmode=require
SPY_WEB_ADDR=127.0.0.1:8097
ACCESS_TEAM=FILL_ME
ACCESS_AUD=FILL_ME
OPS_ADDR=127.0.0.1:9116" spy
    start spy-numbers spy-numbers
    start spy-web spy-web
}

# ---- Launch on the data box ------------------------------------------------

launch_src="$repo/launch"

# launch_web: Launch, the only app that writes to the ad networks
# (launch/README.md). It owns the launch and launch_api schemas and runs their
# migrations at start; launch_api_read must exist before the first one, which
# grants it. Everything it makes is paused. People reach it through the
# Cloudflare tunnel at /launch (platform/OPERATIONS.md).
launch_web() {
    [ -d "$launch_src/deploy" ] || { echo "$launch_src is missing: run setup.sh from a checkout of the repository" >&2; exit 1; }
    install_bin launch-web
    install -m 0644 "$launch_src/deploy/launch-web.service" /etc/systemd/system/launch-web.service
    systemctl daemon-reload

    say "the launch login"
    local r
    for r in launch_api_read intel_api_read; do
        psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$r') THEN CREATE ROLE $r NOLOGIN; END IF; END \$\$"
    done
    local launch_pw
    launch_pw=$(login launch)
    psql_su -d adhunters -c "GRANT CREATE ON DATABASE adhunters TO launch"
    # The Groups, Campaigns and Ads tables show Intel's numbers (intel_api views).
    psql_su -d adhunters -c "GRANT intel_api_read TO launch"

    local settings
    settings=$(grep -E '^#?[A-Z0-9_]+=' "$launch_src/deploy/launch-web.env.example")
    if [ -n "$launch_pw" ]; then
        settings=$(printf '%s\n' "$settings" | sed "s|^DATABASE_URL=.*|DATABASE_URL=postgres://launch:$launch_pw@localhost:5432/adhunters?sslmode=require|")
    fi
    env_file launch-web "$settings" root
    start launch-web launch-web
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
        "hostssl adhunters raposa $worker_ip/32 scram-sha-256" \
        "hostssl adhunters tracks_walker $worker_ip/32 scram-sha-256"; do
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
    # The bridge runs only during the switch-over, started by hand
    # (platform/SWITCH-OVER.md), so it is installed and never enabled here.
    install_bin tracks-bridge
    install_unit tracks-bridge.service
    systemctl daemon-reload

    say "database adhunters and the tracks logins"
    [ "$(psql_su -c "SELECT 1 FROM pg_database WHERE datname = 'adhunters'")" = 1 ] ||
        psql_su -c "CREATE DATABASE adhunters"
    psql_su -d adhunters -c "ALTER DATABASE adhunters SET timezone = 'UTC'"
    psql_su -d adhunters -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"
    psql_su -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tracks_api_read') THEN CREATE ROLE tracks_api_read NOLOGIN; END IF; END \$\$"

    local loader_pw shipper_pw walker_pw
    loader_pw=$(login tracks_loader)
    shipper_pw=$(login tracks_shipper)
    # Before the loader starts: its migrations grant the walker its tables.
    walker_pw=$(login tracks_walker)
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

    if [ -n "$walker_pw" ]; then
        todo+=("put this line in /etc/adhunters/tracks-walker.env on the worker box (shown once):
    DATABASE_URL=postgres://tracks_walker:$walker_pw@$data_ip:5432/adhunters?sslmode=require")
    fi

    env_file tracks-bridge "# tracks-bridge settings (platform/SWITCH-OVER.md). The database
# login and archive keys come from tracks-loader.env.
OLD_DATABASE_URL=FILL_ME
BRIDGE_FROM=FILL_ME
OPS_ADDR=127.0.0.1:9108"

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

    # The 08:00 digest, the Sentry relay, the credit checks and the policy
    # watch (platform/observe).
    install_bin observe-bot
    install_unit observe-bot.service
    systemctl daemon-reload
    env_file observe-bot "$observe_bot_env"
    # Written once: the owner fills in keys and dates. No secrets in it (the
    # keys are in observe-bot.env), and observe-bot runs as a dynamic user.
    if [ ! -f /etc/adhunters/credits.conf ]; then
        install -m 0644 "$here/../observe/credits.conf.example" /etc/adhunters/credits.conf
        say "wrote /etc/adhunters/credits.conf"
    fi
    start observe-bot observe-bot
}

observe_bot_env='# observe-bot settings (see platform/observe/README.md).
TELEGRAM_BOT_TOKEN=FILL_ME
TELEGRAM_CHAT_ID=FILL_ME
# The ops group "AdHunters operation", for Taboola policy changes. Empty: they
# go to TELEGRAM_CHAT_ID.
OPS_TELEGRAM_CHAT_ID=
# The stack'"'"'s Prometheus URL followed by /api/prom, its user number, and a
# token with metrics:read.
GRAFANA_QUERY_URL=FILL_ME
GRAFANA_QUERY_USER=FILL_ME
GRAFANA_QUERY_TOKEN=FILL_ME
# Sentry, read-only (an internal integration token with Issue & Event: Read).
# Leave SENTRY_API_TOKEN empty to keep the relay off.
# Ours (30 Sep 2026): https://de.sentry.io, org marcos-capistrano, project go.
SENTRY_URL=https://de.sentry.io
SENTRY_ORG=FILL_ME
SENTRY_PROJECT=go
SENTRY_API_TOKEN=FILL_ME
# Keys for the balance checks in /etc/adhunters/credits.conf. A check whose
# key is FILL_ME is off.
IPROYAL_API_TOKEN=FILL_ME
# The policy watch: help center collections to crawl, space separated.
# Default: Policy & Content Review.
# POLICY_URLS=https://realize.com/help/en/collections/11915686-policy-content-review
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

# pgbackrest_locked COMMAND: runs a pgbackrest command for the stanza as
# postgres. While an archive-push or a backup holds pgBackRest's lock, the
# command ends with exit 50; it is tried again every 5 s for up to
# PGBACKREST_LOCK_WAIT seconds (default 300), then fails as before.
pgbackrest_locked() {
    local waited=0 limit=${PGBACKREST_LOCK_WAIT:-300} rc
    while :; do
        rc=0
        sudo -u postgres pgbackrest --stanza=adhunters "$@" || rc=$?
        [ "$rc" -eq 50 ] || return "$rc"
        if [ "$waited" -ge "$limit" ]; then
            echo "pgbackrest $1: still locked after ${limit}s (another pgbackrest is running: systemctl status 'pgbackrest-*'); run setup.sh again later" >&2
            return "$rc"
        fi
        say "pgbackrest $1: locked by another pgbackrest, trying again in 5 s"
        sleep 5
        waited=$((waited + 5))
    done
}

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
    pgbackrest_locked stanza-create
    pgbackrest_locked check
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
observe-bot             9107  observe-bot     -
tracks-bridge           9108  tracks-bridge   -
create-web              9109  create-web      -
library                 9110  library         -
launch-web              9111  launch-web      -
create                  9112  create          -
funnels-edge            9117  funnels-edge    -
funnels-loader          9118  funnels-loader  -
funnels-web             9123  funnels-web     -
intel-collect           9113  intel-collect   -
intel-numbers           9114  intel-numbers   -
intel-web               9115  intel-web       -
desk-agent              9120  desk-agent      -
desk-web                9121  desk-web        -
tracks-walker           9119  tracks-walker   -
spy-web                 9116  spy-web         -
spy-numbers             9122  spy-numbers     -'

alloy_env='# Grafana Alloy settings (root only); see platform/observe/README.md.
# Push URLs: Prometheus ends in /api/prom/push, Loki in /loki/api/v1/push.
GRAFANA_METRICS_URL=FILL_ME
GRAFANA_METRICS_USER=FILL_ME
GRAFANA_LOGS_URL=FILL_ME
GRAFANA_LOGS_USER=FILL_ME
GRAFANA_CLOUD_TOKEN=FILL_ME'

# services_alloy: the scrape block for the units enabled on this box. What
# goes on to Grafana Cloud is common.alloy's keep-list for services.
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
  forward_to = [prometheus.relabel.services.receiver]
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
        put "$dir/postgres-queries.yaml" "$(cat "$here/../observe/alloy/postgres-queries.yaml")" && changed=1
        put "$dir/tunnel.alloy" "$(cat "$here/../observe/alloy/tunnel.alloy")" && changed=1
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
    walker_box
    raposa_box
    ;;
standby) capture_box standby:1:9101 ;;
data)
    data_box && backups
    library_box
    create_box
    create_web
    funnels_box
    intel_box
    desk_box
    spy_box
    launch_web
    ;;
esac
alloy_agent

say "done: $role"
if [ ${#todo[@]} -gt 0 ]; then
    echo
    echo "Still to do on this box:"
    for t in "${todo[@]}"; do echo "  - $t"; done
fi

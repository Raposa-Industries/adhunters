#!/usr/bin/env bash
# Deploys the build in ~/adhunters on this box, as root (deploy.sh runs it):
# setup.sh installs it, then every service of ours that should run must be
# running a minute later without restarting. If setup.sh fails or a service
# does not come back, the build that ran before is put back, those services
# are restarted and the script exits 1.
#
# It does not undo migrations (they only add: AGENTS.md) nor settings files
# setup.sh wrote. setup.sh's own output goes to /var/log/adhunters/, root
# only; only its "==" lines and what is still to do are printed.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
bin=/opt/adhunters/bin
prev=/var/lib/adhunters/deploy-prev
log=/var/log/adhunters/setup-$(date -u +%Y%m%dT%H%M%SZ).log
# The services setup.sh installs from bin/; Postgres, Alloy and cloudflared
# are not ours to roll back.
ours='^(tracks-.*|raposa-.*|spy-.*|intel-.*|funnels-.*|desk-.*|launch-.*|create|create-web|library|observe-bot)\.service$'

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }

# units STATE: our long-running services in STATE (one-shot jobs come and go).
units() {
    local u
    for u in $(systemctl list-units --type=service --all --no-legend --plain --state="$1" | awk '{print $1}' | grep -E "$ours" || true); do
        [ "$(systemctl show -p Type --value "$u")" = oneshot ] || echo "$u"
    done
}
restarts() { systemctl show -p NRestarts --value "$1"; }

before=$(units active)

# The running build, as hard links: install_bin puts each new binary in a new
# file, so these keep the old ones.
rm -rf "$prev"
install -d -m 0755 "$prev"
find "$bin" -maxdepth 1 -type f ! -name '*.prev' -exec ln {} "$prev/" \;

rollback() {
    local f u
    echo "== putting the previous build back"
    for f in "$prev"/*; do
        install -m 0755 "$f" "$bin/$(basename "$f").new"
        mv "$bin/$(basename "$f").new" "$bin/$(basename "$f")"
    done
    for u in $before; do systemctl restart "$u" || true; done
    sleep 20
    for u in $before; do echo "   $u $(systemctl is-active "$u" || true)"; done
}

# check: every service that ran before or runs now is running a minute after
# setup.sh, and did not restart in the last 30 seconds (a crash loop).
check() {
    local u bad=0 watch
    declare -A n
    sleep 30
    # shellcheck disable=SC2046 # one unit name per word
    watch=$(printf '%s\n' $before $(units active) $(units activating) | sed '/^$/d' | sort -u)
    for u in $watch; do n[$u]=$(restarts "$u"); done
    sleep 30
    for u in $watch; do
        if ! systemctl is-active --quiet "$u"; then
            echo "   not running: $u ($(systemctl is-active "$u" || true))"
            bad=1
        elif [ "$(restarts "$u")" != "${n[$u]}" ]; then
            echo "   restarting: $u"
            bad=1
        fi
    done
    return $bad
}

install -d -m 0700 /var/log/adhunters
echo "== setup.sh (whole output: $log)"
if ! "$here/setup.sh" --bin "$here/../../bin" >"$log" 2>&1; then
    chmod 0600 "$log"
    grep -E '^== ' "$log" | tail -5 || true
    echo "== setup.sh failed: $(tail -1 "$log")"
    rollback
    exit 1
fi
chmod 0600 "$log"
grep -E '^(== |Still to do|  - )' "$log" || true

echo "== checking the services"
if ! check; then
    rollback
    exit 1
fi
echo "== $(hostname): deployed, $(echo "$before" | wc -w) services running"

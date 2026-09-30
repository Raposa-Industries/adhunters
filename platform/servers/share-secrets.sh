#!/usr/bin/env bash
# Fills in the settings the worker and standby boxes share with the data box,
# without anything secret showing on screen. Run it on your own computer
# (Tailscale up), from the repository, after setup.sh has run on the data box
# and on the boxes named:
#
#   platform/servers/share-secrets.sh [worker] [standby]    (default: both)
#
# It copies the object storage keys from the data box's tracks-loader.env,
# and fills in DATABASE_URL for tracks-shipper (tracks_shipper login) and, on
# the worker, raposa-engine and raposa-web (raposa login) and tracks-walker
# (tracks_walker login). A login's password
# is copied from a box that already has it; the first time, the login gets a
# new random password on the data box. Units whose settings are then
# complete are restarted. A box whose setup has not run yet is skipped, so
# running it again later for that box is fine.
set -euo pipefail

data=admin@adhunters-data
data_ip=10.20.1.20
ssh_opts=(-o ConnectTimeout=15 -o BatchMode=yes)
boxes=("$@")
[ ${#boxes[@]} -gt 0 ] || boxes=(worker standby)
for b in "${boxes[@]}"; do
    case "$b" in worker | standby) ;; *) echo "usage: $0 [worker] [standby]" >&2; exit 2 ;; esac
done

say() { echo "== $*"; }
on() { local h=$1; shift; ssh "${ssh_opts[@]}" "admin@adhunters-$h" "$@"; }

# setting BOX FILE KEY: prints KEY's value in /etc/adhunters/FILE on BOX, or
# nothing when the box, the file or a real value is missing.
setting() {
    on "$1" "sudo sed -n 's/^$3=//p' /etc/adhunters/$2 2>/dev/null" 2>/dev/null | grep -v FILL_ME || true
}

has_setup() { on "$1" "test -f /etc/adhunters/tracks-shipper.env" 2>/dev/null; }

say "object storage keys from the data box"
access=$(setting data tracks-loader.env S3_ACCESS_KEY)
secret=$(setting data tracks-loader.env S3_SECRET_KEY)
[ -n "$access" ] && [ -n "$secret" ] || {
    echo "the data box's /etc/adhunters/tracks-loader.env has no S3 keys yet: fill them in there first" >&2
    exit 1
}

# url LOGIN BOX:FILE...: the login's DATABASE_URL from the first listed file
# that has one, or a new password set on the data box.
url() {
    local login=$1 spec u pw
    shift
    for spec in "$@"; do
        u=$(setting "${spec%%:*}" "${spec#*:}" DATABASE_URL)
        case "$u" in "postgres://$login:"*) echo "$u"; return ;; esac
    done
    pw=$(openssl rand -hex 24)
    printf "ALTER ROLE %s PASSWORD '%s';\n" "$login" "$pw" |
        ssh "${ssh_opts[@]}" "$data" "sudo -u postgres psql -v ON_ERROR_STOP=1 -q" >/dev/null
    echo "postgres://$login:$pw@$data_ip:5432/adhunters?sslmode=require"
    say "gave $login a new password" >&2
}

# The script each box runs: set KEY=VALUE lines in a file, keeping its owner
# and mode, then restart the units whose settings are complete.
remote_lib='set -euo pipefail
setv() {
    local f=/etc/adhunters/$1 k=$2 v=$3 tmp
    tmp=$(mktemp)
    awk -v k="$k" -v v="$v" '"'"'index($0, k "=") == 1 { print k "=" v; done = 1; next } { print } END { if (!done) print k "=" v }'"'"' "$f" >"$tmp"
    cat "$tmp" >"$f"
    rm -f "$tmp"
}
restart() { # restart UNIT FILE [NEEDS]
    if grep -q FILL_ME "/etc/adhunters/$2"; then echo "== $1: settings still incomplete"; return; fi
    if [ -n "${3:-}" ] && [ ! -s "$3" ]; then echo "== $1: waiting for $3"; return; fi
    systemctl restart "$1" && echo "== restarted $1 on $(hostname)"
}'

for box in "${boxes[@]}"; do
    if ! has_setup "$box"; then
        say "$box: skipped (unreachable, or setup.sh has not run there yet)"
        continue
    fi
    say "$box"
    shipper=$(url tracks_shipper worker:tracks-shipper.env standby:tracks-shipper.env)
    script="$remote_lib"
    script+=$'\n'"$(printf 'setv tracks-shipper.env %s %q\n' DATABASE_URL "$shipper" S3_ACCESS_KEY "$access" S3_SECRET_KEY "$secret")"
    script+=$'\nrestart tracks-shipper tracks-shipper.env'
    if [ "$box" = worker ]; then
        raposa=$(url raposa worker:raposa-engine.env worker:raposa-web.env)
        for f in raposa-engine.env raposa-web.env; do
            # S3_ENDPOINT and S3_REGION too: setup.sh before 29 Sep 2026 left
            # every S3_ line out of the raposa files.
            script+=$'\n'"$(printf "setv $f %s %q\n" DATABASE_URL "$raposa" S3_ENDPOINT fsn1.your-objectstorage.com S3_REGION fsn1 S3_ACCESS_KEY "$access" S3_SECRET_KEY "$secret")"
        done
        script+=$'\nrestart raposa-engine raposa-engine.env /etc/adhunters/raposa/proxies.env'
        script+=$'\nrestart raposa-web raposa-web.env'
        if on worker "test -f /etc/adhunters/tracks-walker.env" 2>/dev/null; then
            walker=$(url tracks_walker worker:tracks-walker.env)
            script+=$'\n'"$(printf 'setv tracks-walker.env %s %q\n' DATABASE_URL "$walker" S3_ACCESS_KEY "$access" S3_SECRET_KEY "$secret")"
            script+=$'\nrestart tracks-walker tracks-walker.env /etc/adhunters/tracks-capture/proxies.env'
        fi
    fi
    printf '%s\n' "$script" | on "$box" "sudo bash -s"
done
say "done"

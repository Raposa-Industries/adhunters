#!/usr/bin/env bash
# Puts the Sentry DSN on every box and restarts what reads it, without the
# DSN showing on screen. Run it on your own computer (Tailscale up), from the
# repository, after setup.sh has run on the boxes:
#
#   platform/servers/set-sentry-dsn.sh [data] [worker] [standby]    (default: all three)
#
# It asks for the DSN (Sentry: the project's Settings, Client Keys), writes it
# as SENTRY_DSN in /etc/adhunters/observe.env on each box, then restarts only
# the units that read that file and are already running, capture instances
# one at a time, so collection never stops and nothing stopped on purpose
# (tracks-bridge, a disabled raposa-engine) is started. Running it again
# with the same DSN restarts nothing.
set -euo pipefail

ssh_opts=(-o ConnectTimeout=15 -o BatchMode=yes)
boxes=("$@")
[ ${#boxes[@]} -gt 0 ] || boxes=(data worker standby)
for b in "${boxes[@]}"; do
    case "$b" in data | worker | standby) ;; *) echo "usage: $0 [data] [worker] [standby]" >&2; exit 2 ;; esac
done

read -rsp "Sentry DSN (hidden): " dsn
echo
case "$dsn" in
https://*@*.sentry.io/*) ;;
*) echo "that does not look like a DSN (https://KEY@oNNN.ingest....sentry.io/NNN)" >&2; exit 1 ;;
esac

# Runs on each box as root; the DSN arrives on the first line of stdin, so it
# never appears in a command line or the process list.
remote='set -euo pipefail
read -r dsn
f=/etc/adhunters/observe.env
[ -f "$f" ] || { echo "== $(hostname): no $f, run setup.sh first"; exit 0; }
if grep -qxF "SENTRY_DSN=$dsn" "$f"; then echo "== $(hostname): already set"; exit 0; fi
tmp=$(mktemp)
awk -v v="$dsn" '"'"'index($0, "SENTRY_DSN=") == 1 { print "SENTRY_DSN=" v; done = 1; next } { print } END { if (!done) print "SENTRY_DSN=" v }'"'"' "$f" >"$tmp"
cat "$tmp" >"$f"
rm -f "$tmp"
echo "== $(hostname): SENTRY_DSN set"
for u in $(systemctl list-units --state=running --plain --no-legend "tracks-*" "raposa-engine*" "raposa-web*" "observe-bot*" | awk "{print \$1}"); do
    grep -q observe.env "/etc/systemd/system/$u" "/etc/systemd/system/${u%%@*}@.service" 2>/dev/null || continue
    systemctl restart "$u" && echo "== restarted $u"
    case "$u" in tracks-capture@*) sleep 15 ;; esac
done'

for box in "${boxes[@]}"; do
    printf '%s\n' "$dsn" | ssh "${ssh_opts[@]}" "admin@adhunters-$box" "sudo bash -c $(printf %q "$remote")" ||
        echo "== $box: unreachable, run this again for it later"
done
echo "== done: try it on the data box with observe-bot sentry-test (platform/observe/README.md)"

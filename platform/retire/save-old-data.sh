#!/usr/bin/env bash
# Saves what lives only on the old boxes (prodbox, bigworker) into the archive
# bucket before either is stopped. It reads the old boxes and never changes
# them. Run it on your own computer (Tailscale up), from the repository:
#
#   platform/retire/save-old-data.sh check          what is there, what can run
#   platform/retire/save-old-data.sh setup          rclone on the data box
#   platform/retire/save-old-data.sh db             old database -> bucket (runs on the data box)
#   platform/retire/save-old-data.sh gz             bigworker's archived sighting days -> bucket
#   platform/retire/save-old-data.sh auto-creative  Auto-Creative's database and files -> bucket (needs ssh prod)
#
# Everything lands under adhunters-raw/legacy/ (see README.md). Each step
# skips what the bucket already has, so it can be run again.
set -euo pipefail

data=admin@adhunters-data
ssh_opts=(-o ConnectTimeout=15 -o BatchMode=yes)
here=$(cd "$(dirname "$0")" && pwd)
day=$(date -u +%Y%m%d)

say() { echo "== $*"; }
on_data() { ssh "${ssh_opts[@]}" "$data" "$@"; }

# The env file on the data box that holds a working OLD_DATABASE_URL: the
# bridge's (the collector's own login) or spy-numbers' (read only).
old_env() {
    on_data 'for f in /etc/adhunters/tracks-bridge.env /etc/adhunters/spy-numbers.env; do
        sudo grep -q "^OLD_DATABASE_URL=postgres" "$f" 2>/dev/null && { echo "$f"; exit 0; }
    done; exit 1'
}

case "${1:-}" in
check)
    say "data box: tools"
    on_data 'command -v rclone || echo "rclone: not installed (run: setup)"; pg_dump --version; sudo test -s /root/.config/rclone/rclone.conf && echo "rclone remote: set" || echo "rclone remote: not set (run: setup)"; df -h /var/tmp | tail -1'
    say "data box: a login on the old database"
    if f=$(old_env); then
        echo "found in $f"
        on_data "sudo bash -c 'set -a; . $f; set +a; PGOPTIONS=\"-c default_transaction_read_only=on -c statement_timeout=0\" psql \"\$OLD_DATABASE_URL\" -X -q -At -v ON_ERROR_STOP=1'" <<'SQL'
SELECT 'login ' || current_user || ' on ' || inet_server_addr() || ', ' || pg_size_pretty(pg_database_size(current_database()));
SELECT 'tables it cannot read: ' || count(*) FILTER (WHERE NOT has_table_privilege(c.oid, 'SELECT')) || ' of ' || count(*)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p') AND n.nspname NOT IN ('pg_catalog', 'information_schema');
SELECT 'spy.sighting days: ' || count(*) || ', ' || min(c.relname) || ' to ' || max(c.relname)
FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid WHERE i.inhparent = 'spy.sighting'::regclass;
SQL
    else
        echo "none: neither tracks-bridge.env nor spy-numbers.env has OLD_DATABASE_URL (see README.md, 'A login on the old database')"
    fi
    say "bigworker: archived sighting days"
    ssh "${ssh_opts[@]}" bigworker 'ls -l --time-style=+%F /opt/backups/spy-sightings; for f in /opt/backups/spy-sightings/*.tsv.gz; do echo "$(basename "$f"): $(( $(zcat "$f" | wc -l) - 1 )) rows"; done; journalctl -u spy-archive --since -10days --no-pager -o short-iso | grep -iE "archiv|drop|error|fail" | tail -15'
    ;;
setup)
    say "rclone and its archive remote on the data box (keys from tracks-loader.env, never shown)"
    on_data 'sudo bash -s' <<'EOF2'
set -euo pipefail
command -v rclone >/dev/null || apt-get install -y -q rclone >/dev/null || { apt-get update -q >/dev/null && apt-get install -y -q rclone >/dev/null; }
set -a; . /etc/adhunters/tracks-loader.env; set +a
endpoint=${S3_ENDPOINT:-fsn1.your-objectstorage.com}
endpoint=${endpoint#https://}
umask 077
mkdir -p /root/.config/rclone
cat > /root/.config/rclone/rclone.conf <<CONF
[archive]
type = s3
provider = Other
endpoint = https://$endpoint
region = ${S3_REGION:-fsn1}
access_key_id = $S3_ACCESS_KEY
secret_access_key = $S3_SECRET_KEY
CONF
rclone --config /root/.config/rclone/rclone.conf lsd archive: 
EOF2
    ;;
db)
    f=$(old_env) || { echo "no OLD_DATABASE_URL on the data box; run check" >&2; exit 1; }
    say "copying save-db.sh to the data box and starting it there (unit retire-save-db)"
    on_data 'sudo install -d -m 700 /var/tmp/retire && sudo tee /var/tmp/retire/save-db.sh >/dev/null' < "$here/save-db.sh"
    on_data "sudo systemctl reset-failed retire-save-db 2>/dev/null; sudo systemd-run --unit=retire-save-db --description='Save the old database to the archive' \
        -p EnvironmentFile=$f --setenv=RCLONE_CONFIG=/root/.config/rclone/rclone.conf --setenv=FRESH_MAIN=${FRESH_MAIN:-} \
        -p Nice=10 /bin/bash /var/tmp/retire/save-db.sh"
    echo "Follow it:  ssh $data 'sudo journalctl -u retire-save-db -f -o cat'"
    ;;
gz)
    say "bigworker's /opt/backups/spy-sightings -> adhunters-raw/legacy/spy-sightings"
    have=$(on_data 'sudo rclone --config /root/.config/rclone/rclone.conf lsf archive:adhunters-raw/legacy/spy-sightings 2>/dev/null' || true)
    for f in $(ssh "${ssh_opts[@]}" bigworker 'cd /opt/backups/spy-sightings && ls *.tsv.gz'); do
        if grep -qx "$f" <<<"$have"; then echo "already saved: $f"; continue; fi
        ssh "${ssh_opts[@]}" bigworker "cat /opt/backups/spy-sightings/$f" \
            | on_data "sudo bash -c 'umask 077; cat > /var/tmp/retire-$f'"
        ssh "${ssh_opts[@]}" bigworker "cat /opt/backups/spy-sightings/$f.sha256" \
            | on_data "sudo bash -c 'cd /var/tmp && sed \"s/ $f\$/ retire-$f/\" | sha256sum -c --quiet && \
                rclone --config /root/.config/rclone/rclone.conf copyto /var/tmp/retire-$f archive:adhunters-raw/legacy/spy-sightings/$f && \
                echo \$(sha256sum < /var/tmp/retire-$f | cut -d\" \" -f1)\"  $f\" | rclone --config /root/.config/rclone/rclone.conf rcat archive:adhunters-raw/legacy/spy-sightings/$f.sha256; \
                rc=\$?; rm -f /var/tmp/retire-$f; exit \$rc'"
        echo "saved $f (checksum matched bigworker's)"
    done
    ;;
auto-creative)
    # prodbox may want a password, so its ssh is opened once here, where it
    # can ask, and both copies below ride that connection (15 minutes).
    prod_ssh=(ssh -o ConnectTimeout=15 -o ControlMaster=auto -o "ControlPath=$HOME/.ssh/retire-%C" -o ControlPersist=15m)
    say "connecting to prodbox (asks for its password once)"
    "${prod_ssh[@]}" prod true
    say "Auto-Creative on prodbox: its database (pg_dump) and its MinIO volume (tar) -> adhunters-raw/legacy/auto-creative"
    dest=archive:adhunters-raw/legacy/auto-creative
    "${prod_ssh[@]}" prod "docker exec auto-creative-postgres-1 sh -c 'pg_dump -U \"\$POSTGRES_USER\" -Fc \"\$POSTGRES_DB\"'" \
        | on_data "sudo rclone --config /root/.config/rclone/rclone.conf rcat $dest/autocreative-$day.dump"
    on_data "sudo rclone --config /root/.config/rclone/rclone.conf cat $dest/autocreative-$day.dump | pg_restore -l | grep -c 'TABLE DATA'" | sed 's/^/tables with data: /'
    "${prod_ssh[@]}" prod 'tar -C "$(docker volume inspect -f "{{.Mountpoint}}" auto-creative_miniodata)" --warning=no-file-changed -czf - .; [ $? -le 1 ]' \
        | on_data "sudo rclone --config /root/.config/rclone/rclone.conf rcat $dest/miniodata-$day.tar.gz"
    on_data "sudo rclone --config /root/.config/rclone/rclone.conf cat $dest/miniodata-$day.tar.gz | tar -tzf - | wc -l" | sed 's/^/files in the MinIO copy: /'
    on_data "sudo rclone --config /root/.config/rclone/rclone.conf ls $dest"
    ;;
*)
    sed -n '2,13p' "$0"; exit 2 ;;
esac

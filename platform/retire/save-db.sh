#!/usr/bin/env bash
# Runs ON THE DATA BOX, as root, started by save-old-data.sh (systemd-run, so
# it keeps going if your computer sleeps). It copies the old collector's
# database on prodbox into the archive bucket, reading it and never writing it:
#
#   adhunters-raw/legacy/prodbox/adplatform_v2-main-<day>.dump
#       pg_dump -Fc of everything except the two sighting tables (~13 GB raw)
#   adhunters-raw/legacy/prodbox/adplatform_v2-adhunters_sighting.dump
#       the legacy public.adhunters_sighting (26 GB raw; nothing writes it)
#   adhunters-raw/legacy/prodbox/adplatform_v2-spy_sighting-schema.sql
#   adhunters-raw/legacy/spy-sightings/sighting_<yyyymmdd>.tsv.gz (+ .sha256)
#       each closed day of spy.sighting, in the collector's own archive format
#       (collector-cli db archive-sightings): a "# columns" line, then COPY text
#
# Anything already in the bucket is skipped, so running it again only adds
# the days closed since: run it once a week until the collector stops,
# because the collector drops each day 14 days after it closes
# (spy-archive.timer). FRESH_MAIN=1 also makes a new main dump.
#
# spy.sighting is left out of the main dump on purpose: the collector creates
# a partition near 00:00 UTC and drops one at 04:10 UTC, and both would wait
# behind a long dump's lock and stall its writes. The days are copied one
# partition at a time instead, each in a few minutes.
set -euo pipefail

: "${OLD_DATABASE_URL:?OLD_DATABASE_URL is not set}"
export PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=0'
day=$(date -u +%Y%m%d)
dumps=archive:adhunters-raw/legacy/prodbox
days=archive:adhunters-raw/legacy/spy-sightings
tmp=$(mktemp -d /var/tmp/retire-save.XXXXXX)
trap 'rm -rf "$tmp"' EXIT

say() { echo "== $(date -u +%H:%M:%S) $*"; }
have() { [ -n "$(rclone lsf "$1" 2>/dev/null)" ]; }
old() { psql "$OLD_DATABASE_URL" -X -q -At -v ON_ERROR_STOP=1 "$@"; }

# dump NAME PG_DUMP_ARGS...: streams a pg_dump into the bucket, reads it all
# back through pg_restore, and only then writes NAME.dump.ok next to it. A
# dump without its .ok (cut off half way) is made again on the next run.
dump() {
    local name=$1 obj; shift
    obj=$dumps/adplatform_v2-$name.dump
    if have "$obj.ok"; then say "already saved: $obj"; return; fi
    say "dumping $name"
    pg_dump -d "$OLD_DATABASE_URL" -Fc -Z 6 "$@" | rclone rcat --s3-chunk-size 64Mi "$obj"
    say "reading $name back"
    rclone cat "$obj" | pg_restore -f /dev/null
    rclone lsl "$obj" | rclone rcat "$obj.ok"
    say "saved $(rclone lsl "$obj")"
}

who=$(old -c "SELECT current_user || ', database ' || pg_size_pretty(pg_database_size(current_database()))")
say "connected as $who"

# The main dump is made once; FRESH_MAIN=1 makes another, dated today (the
# final one, after the collector stops writing).
if [ "${FRESH_MAIN:-}" = 1 ] || [ -z "$(rclone lsf "$dumps" --include 'adplatform_v2-main-*.dump.ok' 2>/dev/null)" ]; then
    dump "main-$day" --exclude-table-and-children=spy.sighting --exclude-table-and-children=public.adhunters_sighting
else
    say "main dump already saved (FRESH_MAIN=1 for a new one)"
fi
dump adhunters_sighting --table-and-children=public.adhunters_sighting

schema=$dumps/adplatform_v2-spy_sighting-schema.sql
if ! have "$schema"; then
    pg_dump -d "$OLD_DATABASE_URL" --schema-only --table=spy.sighting | rclone rcat "$schema"
    say "saved $schema"
fi

# The closed days: every partition whose range ended more than an hour ago.
cols='seen_at, scrape_id, ad_id, creative_id, publisher_id, placement_id, campaign_id, link_id, site_id, ecpa_percentile, device_id, feed_position, block_position'
for p in $(old -c "
    SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'spy.sighting'::regclass
      AND (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'TO \(''([^'']+)''\)'))[1]::timestamptz < now() - interval '1 hour'
    ORDER BY 1"); do
    obj=$days/$p.tsv.gz
    if have "$obj"; then say "already saved: $p"; continue; fi
    want=$(old -c "SELECT count(*) FROM spy.$p")
    say "copying $p ($want rows)"
    { echo "# ${cols//, /$'\t'}"; old -c "COPY spy.$p ($cols) TO STDOUT"; } | gzip -6 > "$tmp/$p.tsv.gz"
    got=$(( $(zcat "$tmp/$p.tsv.gz" | wc -l) - 1 ))
    if [ "$got" != "$want" ]; then
        echo "$p: file has $got rows, table has $want; not saved" >&2
        exit 1
    fi
    (cd "$tmp" && sha256sum "$p.tsv.gz" > "$p.tsv.gz.sha256")
    rclone copyto "$tmp/$p.tsv.gz.sha256" "$obj.sha256"
    rclone copyto --s3-chunk-size 64Mi "$tmp/$p.tsv.gz" "$obj"
    rm -f "$tmp/$p.tsv.gz" "$tmp/$p.tsv.gz.sha256"
    say "saved $p"
done

say "done. In the bucket now:"
rclone ls "$dumps"
echo "spy-sightings days: $(rclone lsf "$days" --include '*.tsv.gz' | wc -l)"

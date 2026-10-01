#!/usr/bin/env bash
# Is everything the old collector holds now in the new apps? Puts the old
# database next to its new home, table by table, with counts on both sides,
# and lists what lies only in the archive. It only reads: both databases in
# read-only transactions, and the archive bucket with rclone. It writes only
# a work folder under /var/tmp, removed at the end.
#
# Run it on the data box as root (it reads OLD_DATABASE_URL from
# tracks-bridge.env), from your computer:
#
#   ssh admin@adhunters-data 'sudo bash -s' < platform/retire/reconcile.sh
#
# Optional: BEFORE=2026-10-02 (the -before the last tracks-loader import-old
# used; days from it on are Tracks' own and are not compared).
set -uo pipefail

before=${BEFORE:-2026-10-02}
work=$(mktemp -d /var/tmp/reconcile.XXXXXX)
chmod 755 "$work"
trap 'rm -rf "$work"' EXIT
set -a; . /etc/adhunters/tracks-bridge.env; set +a
export PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=0"
old() { psql "$OLD_DATABASE_URL" -X -q -At -F, -v ON_ERROR_STOP=1 -v before="$before" "$@"; }
new() { runuser -u postgres -- env PGOPTIONS="$PGOPTIONS" psql -d adhunters -X -q -At -F, -v ON_ERROR_STOP=1 -v before="$before" "$@"; }
say() { printf '\n== %s\n' "$*"; }
row() { printf '%-34s %14s %14s  %s\n' "$@"; }

# keys NAME OLD_SQL NEW_SQL: each side's natural keys, and how many old keys
# are missing from the new side (with up to 5 examples).
keys() {
    local f="$work/${1//[^a-z0-9]/_}" m
    old -c "$2" | LC_ALL=C sort -u > "$f.old"
    new -c "$3" | LC_ALL=C sort -u > "$f.new"
    LC_ALL=C comm -23 "$f.old" "$f.new" > "$f.missing"
    m=$(wc -l < "$f.missing")
    row "$1" "$(wc -l < "$f.old")" "$(wc -l < "$f.new")" "missing in new: $m"
    [ "$m" -gt 0 ] && head -5 "$f.missing" | sed 's/^/      e.g. /'
}

echo "Reconciliation $(date -u +%FT%TZ), old days compared before $before"

say "1. Lookups by natural key (old rows, new rows, old keys missing in new)"
row "" old new ""
keys networks   "SELECT code FROM spy.network" "SELECT code FROM tracks.network"
keys devices    "SELECT code FROM spy.device"  "SELECT code FROM tracks.device"
keys placements "SELECT name FROM spy.placement" "SELECT name FROM tracks.placement"
keys brands     "SELECT name FROM spy.brand" "SELECT name FROM tracks.brand"
keys accounts   "SELECT n.code||':'||a.external_id FROM spy.account a JOIN spy.network n ON n.id=a.network_id" \
                "SELECT n.code||':'||a.external_id FROM tracks.account a JOIN tracks.network n ON n.id=a.network_id"
keys campaigns  "SELECT n.code||':'||c.external_id FROM spy.campaign c JOIN spy.network n ON n.id=c.network_id" \
                "SELECT n.code||':'||c.external_id FROM tracks.campaign c JOIN tracks.network n ON n.id=c.network_id"
keys creatives  "SELECT creative_key FROM spy.creative" "SELECT creative_key FROM tracks.creative"
keys ads        "SELECT c.creative_key||':'||md5(a.headline) FROM spy.ad a JOIN spy.creative c ON c.id=a.creative_id" \
                "SELECT c.creative_key||':'||md5(a.headline) FROM tracks.ad a JOIN tracks.creative c ON c.id=a.creative_id"
keys links      "SELECT link_key FROM spy.link" "SELECT link_key FROM tracks.link"
keys network_ads "SELECT n.code||':'||x.external_id FROM spy.network_ad x JOIN spy.network n ON n.id=x.network_id" \
                 "SELECT n.code||':'||x.external_id FROM tracks.network_ad x JOIN tracks.network n ON n.id=x.network_id"
# Publishers match by name or by one of the collector's other names for them.
old -c "SELECT p.name||','||COALESCE(string_agg(a.alias, ','), '') FROM spy.publisher p LEFT JOIN spy.publisher_alias a ON a.publisher_id=p.id GROUP BY p.id" > "$work/pub.old"
new -c "SELECT name FROM tracks.publisher" > "$work/pub.new"
pm=$(awk -F, 'NR==FNR {have[$0]=1; next} {ok=0; for (i=1; i<=NF; i++) if ($i != "" && have[$i]) ok=1; if (!ok) print $1}' "$work/pub.new" "$work/pub.old")
row publishers "$(wc -l < "$work/pub.old")" "$(wc -l < "$work/pub.new")" "missing in new: $(printf '%s' "$pm" | grep -c . )"
[ -n "$pm" ] && printf '%s\n' "$pm" | head -5 | sed 's/^/      e.g. /'

say "2. Counts per day (sum of sightings or scrapes; old, new, difference)"
# Sums survive the import's regrouping (publisher aliases merge rows), so
# they must match exactly; row counts may not.
old > "$work/day.old" <<'SQL'
SELECT d, m, v FROM (
  SELECT hour::date AS d, 'ad_hourly sightings' AS m, sum(sightings)::bigint AS v FROM spy.ad_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'ad_hourly scrapes', sum(scrapes) FROM spy.ad_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'acct_brand_hourly sightings', sum(sightings) FROM spy.ad_account_brand_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'publisher_hourly scrapes', sum(scrapes) FROM spy.publisher_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'publisher_hourly sightings', sum(sightings) FROM spy.publisher_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'hours with scrapes', count(DISTINCT hour) FROM spy.publisher_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT day, 'ad_daily sightings', sum(sightings) FROM spy.ad_daily WHERE day < :'before' GROUP BY 1
  UNION ALL SELECT day, 'ad_daily scrapes', sum(scrapes) FROM spy.ad_daily WHERE day < :'before' GROUP BY 1
  UNION ALL SELECT day, 'ad_account_daily sightings', sum(sightings) FROM spy.ad_account_daily WHERE day < :'before' GROUP BY 1
  UNION ALL SELECT day, 'placement_daily sightings', sum(sightings) FROM spy.placement_daily WHERE day < :'before' GROUP BY 1
  UNION ALL SELECT day, 'campaign_daily sightings', sum(sightings) FROM spy.campaign_daily WHERE day < :'before' GROUP BY 1
) x ORDER BY 1, 2;
SQL
new > "$work/day.new" <<'SQL'
SELECT d, m, v FROM (
  SELECT hour::date AS d, 'ad_hourly sightings' AS m, sum(sightings)::bigint AS v FROM tracks.ad_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'ad_hourly scrapes', sum(scrapes) FROM tracks.ad_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'acct_brand_hourly sightings', sum(sightings) FROM tracks.ad_account_brand_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'publisher_hourly scrapes', sum(scrapes) FROM tracks.publisher_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'publisher_hourly sightings', sum(sightings) FROM tracks.publisher_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'hours with scrapes', count(DISTINCT hour) FROM tracks.publisher_hourly WHERE hour < :'before' GROUP BY 1
  UNION ALL SELECT hour::date, 'hours marked imported', count(*) FROM tracks.hour_state WHERE imported_at IS NOT NULL AND hour < :'before' GROUP BY 1
  UNION ALL SELECT day, 'ad_daily sightings', sum(sightings) FROM tracks.ad_daily WHERE day < :'before' GROUP BY 1
  UNION ALL SELECT day, 'ad_daily scrapes', sum(scrapes) FROM tracks.ad_daily WHERE day < :'before' GROUP BY 1
  UNION ALL SELECT day, 'ad_account_daily sightings', sum(sightings) FROM tracks.ad_account_daily WHERE day < :'before' GROUP BY 1
  UNION ALL SELECT day, 'placement_daily sightings', sum(sightings) FROM tracks.placement_daily WHERE day < :'before' GROUP BY 1
  UNION ALL SELECT day, 'campaign_daily sightings', sum(sightings) FROM tracks.campaign_daily WHERE day < :'before' GROUP BY 1
) x ORDER BY 1, 2;
SQL
awk -F, 'NR==FNR {o[$1","$2]=$3; k[$1","$2]=1; next} {n[$1","$2]=$3; k[$1","$2]=1}
    END {
        for (x in k) {
            split(x, p, ","); d = (n[x]+0) - (o[x]+0)
            flag = (p[2] == "hours marked imported" || d == 0) ? "" : "  <-- differs"
            printf "%s  %-28s %14d %14d %+12d%s\n", p[1], p[2], o[x]+0, n[x]+0, d, flag
        }
    }' "$work/day.old" "$work/day.new" | sort > "$work/day.cmp"
cat "$work/day.cmp"
echo "$(grep -c 'differs' "$work/day.cmp") day/count pairs differ"

say "2b. The last day before $before, before the switch-over at 22:00: creatives each side saw"
last=$(date -u -d "$before - 1 day" +%F)
row "" old new ""
keys "creatives $last <22:00" \
    "SELECT DISTINCT c.creative_key FROM spy.ad_hourly h JOIN spy.ad a ON a.id=h.ad_id JOIN spy.creative c ON c.id=a.creative_id WHERE h.hour >= '$last' AND h.hour < '$last 22:00Z'" \
    "SELECT DISTINCT c.creative_key FROM tracks.ad_hourly h JOIN tracks.ad a ON a.id=h.ad_id JOIN tracks.creative c ON c.id=a.creative_id WHERE h.hour >= '$last' AND h.hour < '$last 22:00Z'"
old -c "SELECT 'old sightings, scrapes <22:00', sum(sightings), sum(scrapes) FROM spy.ad_hourly WHERE hour >= '$last' AND hour < '$last 22:00Z'"
new -c "SELECT 'new sightings, scrapes <22:00', sum(sightings), sum(scrapes) FROM tracks.ad_hourly WHERE hour >= '$last' AND hour < '$last 22:00Z'"

say "3. Hours the collector never counted (09-14 to $before)"
old <<'SQL'
WITH h AS (SELECT generate_series((SELECT min(hour) FROM spy.publisher_hourly), (:'before'::timestamptz - interval '1 hour'), interval '1 hour') AS hour)
SELECT 'hours with no publisher_hourly row', count(*), string_agg(to_char(hour, 'MM-DD HH24'), ' ' ORDER BY hour)
FROM h WHERE NOT EXISTS (SELECT 1 FROM spy.publisher_hourly p WHERE p.hour = h.hour);
SQL
echo "  Of those, scrapes the old database still holds (spy.scrape keeps ~14 days; 0 = never scraped):"
old <<'SQL'
WITH h AS (SELECT generate_series((SELECT min(hour) FROM spy.publisher_hourly), (:'before'::timestamptz - interval '1 hour'), interval '1 hour') AS hour),
gap AS (SELECT hour FROM h WHERE NOT EXISTS (SELECT 1 FROM spy.publisher_hourly p WHERE p.hour = h.hour))
SELECT '  scrape rows in gap hours', count(*), min(s.scraped_at), max(s.scraped_at) FROM gap JOIN spy.scrape s ON s.scraped_at >= gap.hour AND s.scraped_at < gap.hour + interval '1 hour';
SELECT '  oldest scrape row held', min(scraped_at) FROM spy.scrape;
SELECT '  oldest ad_hourly vs ad_daily', (SELECT min(hour) FROM spy.ad_hourly), (SELECT min(day) FROM spy.ad_daily);
SQL

say "4. Creative pairs (one row per pair before $before)"
old <<'SQL'
SELECT 'old creative_link (pairs, sightings)', count(*), COALESCE(sum(sightings),0) FROM spy.creative_link WHERE first_seen_at < :'before';
SELECT 'old creative_campaign (pairs, sightings)', count(*), COALESCE(sum(sightings),0) FROM spy.creative_campaign WHERE first_seen_at < :'before';
SQL
new <<'SQL'
SELECT 'new creative_link_daily (rows, sightings)', count(*), COALESCE(sum(sightings),0) FROM tracks.creative_link_daily WHERE day < :'before';
SELECT 'new creative_campaign_daily (rows, sightings)', count(*), COALESCE(sum(sightings),0) FROM tracks.creative_campaign_daily WHERE day < :'before';
SQL

say "5. Spy's copy (old rows; what spy.import_mark says it copied and skipped)"
old <<'SQL'
SELECT 'old operators', count(*) FROM spy.operator;
SELECT 'old accounts with an operator', count(*) FROM spy.account WHERE operator_id IS NOT NULL;
SELECT 'old sites / clues / site_clues', (SELECT count(*) FROM spy.site), (SELECT count(*) FROM spy.clue), (SELECT count(*) FROM spy.site_clue);
SELECT 'old sellers / agencies / grouping_fix', (SELECT count(*) FROM spy.seller), (SELECT count(*) FROM spy.agency), (SELECT count(*) FROM spy.grouping_fix);
SELECT 'old direction_event', count(*) FROM spy.direction_event;
SELECT 'old creative_vertical', count(*) FROM spy.creative_vertical;
SELECT 'old rtb auctions / with campaign / newest', count(*), count(*) FILTER (WHERE COALESCE(campaign_id,'') <> ''), max(intercepted_at) FROM public.adhunters_rtb_auction_log;
SQL
new -c "SELECT 'import_mark', what, copied, skipped, to_char(done_at, 'MM-DD HH24:MI') FROM spy.import_mark ORDER BY what"
new -c "SELECT 'operators_from', text_value FROM spy.setting WHERE name = 'operators_from'"
new -c "SELECT 'price_day imported rows', count(*) FROM spy.price_day WHERE imported" 2>/dev/null || true

say "6. Landing pages (old walker -> tracks.walk 'old-…')"
old <<'SQL'
SELECT 'old landing_page / versions / uses', (SELECT count(*) FROM spy.landing_page), (SELECT count(*) FROM spy.landing_page_version), (SELECT count(*) FROM spy.landing_page_use);
SELECT 'old newest version / use seen', (SELECT max(last_seen_at) FROM spy.landing_page_version), (SELECT max(last_seen_at) FROM spy.landing_page_use);
SQL
new <<'SQL'
SELECT 'new old-walks / distinct uses / newest', count(*), count(DISTINCT split_part(record_id, '-', 2)), max(at) FROM tracks.walk WHERE record_id LIKE 'old-%';
SELECT 'walks in last 24 h: copied / live', count(*) FILTER (WHERE record_id LIKE 'old-%'), count(*) FILTER (WHERE record_id NOT LIKE 'old-%') FROM tracks.walk WHERE at > now() - interval '24 hours';
SELECT 'page versions', count(*) FROM tracks.page_version;
SQL

say "7. Raposa (old jobs -> raposa.imported_investigation)"
old -c "SELECT 'old raposa_job', status, count(*), max(requested_at) FROM spy.raposa_job GROUP BY status ORDER BY 2"
new -c "SELECT 'imported investigations', count(*), max(imported_at) FROM raposa.imported_investigation"

say "8. What the old database still writes (newest rows)"
old <<'SQL'
SELECT 'scrape, collector', max(scraped_at) FROM spy.scrape WHERE worker_node IS NULL OR worker_node NOT LIKE 'tracks:%';
SELECT 'scrape, bridged from Tracks', max(scraped_at) FROM spy.scrape WHERE worker_node LIKE 'tracks:%';
SELECT 'ad_hourly newest hour', max(hour) FROM spy.ad_hourly;
SELECT 'raposa_job newest', max(requested_at) FROM spy.raposa_job;
SQL

say "9. Old tables, biggest first (live rows, size)"
old -c "SELECT schemaname||'.'||relname, n_live_tup, pg_size_pretty(pg_total_relation_size(relid)) FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC" | awk -F, '{printf "  %-48s %12s  %s\n", $1, $2, $3}' | head -130

say "10. The archive (adhunters-raw/legacy)"
if command -v rclone >/dev/null; then
    rclone lsl archive:adhunters-raw/legacy/prodbox 2>&1 | sort -k4
    echo "spy-sightings days in the bucket:"
    rclone lsf archive:adhunters-raw/legacy/spy-sightings --include '*.tsv.gz' 2>&1 | sed 's/sighting_//; s/.tsv.gz//' | tr '\n' ' '; echo
    echo "spy.sighting days the old database holds:"
    old -c "SELECT string_agg(regexp_replace(c.relname, '\\D', '', 'g'), ' ' ORDER BY c.relname) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid WHERE i.inhparent = 'spy.sighting'::regclass"
    rclone lsl archive:adhunters-raw/legacy/auto-creative 2>&1
    echo "legacy total: $(rclone size archive:adhunters-raw/legacy 2>&1 | tr '\n' ' ')"
else
    echo "rclone is not installed (platform/retire/save-old-data.sh setup)"
fi
echo "retire-save-db, last lines:"
journalctl -u retire-save-db -n 5 -o cat --no-pager 2>&1
echo
echo "Done $(date -u +%FT%TZ)."

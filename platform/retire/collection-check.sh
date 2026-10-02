#!/usr/bin/env bash
# Is data collection running smoothly, and does Tracks collect as much as the
# old collector did? One report: each box (services, restarts, errors logged,
# disk, memory, the spools), the loader, capture per hour, per proxy line and
# per publisher, the walker, Raposa, Spy's refreshes, then Tracks' last closed
# hours next to the old collector's same hours on its last days.
#
# The comparison needs nothing from prodbox: the old collector's days live in
# Tracks' hourly counts since tracks-loader import-old (hour_state.imported_at
# marks them), the same numbers Spy shows.
#
# It only reads: Postgres in read-only transactions, and systemctl, journalctl,
# df and find on the boxes. Run it on your own computer (Tailscale up), from
# the repository:
#
#   platform/retire/collection-check.sh > collection-check.txt
set -uo pipefail

role=${1:-here}

if [ "$role" = here ]; then
    self=$(readlink -f "$0")
    echo "Collection check $(date -u +%FT%TZ)"
    for b in worker standby data; do
        printf '\n######## adhunters-%s\n' "$b"
        ssh -o ConnectTimeout=15 -o BatchMode=yes "admin@adhunters-$b" "sudo bash -s -- $b" < "$self" ||
            echo "(could not run on adhunters-$b)"
    done
    exit 0
fi

# ---- on a box, as root -------------------------------------------------------

export TZ=UTC
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
mask() { sed -E 's#//[^/@ ]+@#//***@#g; s#(password|token|key)=[^ &"]+#\1=***#gI'; }

printf '== %s: up %s, load %s\n' "$(hostname)" "$(uptime -p | sed 's/^up //')" "$(cut -d' ' -f1-3 /proc/loadavg)"
free -m | awk 'NR==2 {printf "memory: %d MB used of %d, %d available\n", $3, $2, $7}'
df -h --output=target,size,used,avail,pcent / /var/lib/tracks /var/lib/postgresql 2>/dev/null | awk '!seen[$0]++'
printf 'out-of-memory kills in 24 h: %s\n' "$(journalctl -k --since -24h -q --no-pager 2>/dev/null | grep -ci 'killed process')"

echo
echo "-- failed units"
systemctl list-units --state=failed --no-legend --plain | sed 's/^/  /'
echo "-- services: state, since, restarts in 24 h, errors and warnings logged in 24 h (UTC)"
printf '  %-28s %-18s %-12s %8s %7s %6s\n' unit state since restarts errors warns
systemctl list-units --type=service --all --no-legend --plain |
    awk '{print $1}' |
    grep -E '^(tracks-|raposa-|spy-|observe-|intel-|create|library|launch|funnels|desk|postgresql|alloy|cloudflared|pgbackrest)' |
    while read -r u; do
        state=$(systemctl show -p ActiveState -p SubState "$u" | sort | sed 's/^[^=]*=//' | paste -sd/)
        since=$(systemctl show -p ActiveEnterTimestamp --value "$u")
        since=$([ -n "$since" ] && date -d "$since" +'%m-%d %H:%M' || echo -)
        journalctl -u "$u" --since -24h -o cat --no-pager -q > "$tmp" 2>/dev/null
        restarts=$(grep -c 'Scheduled restart job' "$tmp")
        errs=$(( $(grep -c '"level":"ERROR"' "$tmp") + $(journalctl -u "$u" --since -24h -p err -q --no-pager 2>/dev/null | wc -l) ))
        warns=$(grep -c '"level":"WARN"' "$tmp")
        printf '  %-28s %-18s %-12s %8s %7s %6s\n' "${u%.service}" "$state" "$since" "$restarts" "$errs" "$warns"
        if [ "$errs" -gt 0 ]; then
            python3 - "$tmp" "$since" <<'PY'
import json, re, sys, collections
path, since = sys.argv[1:3]
start = None if since == "-" else since  # MM-DD HH:MM, compared as text with each line's own time
seen = collections.OrderedDict()
for line in open(path, errors="replace"):
    if not line.startswith("{") or '"level":"ERROR"' not in line:
        continue
    try:
        e = json.loads(line)
    except ValueError:
        continue
    key = e.get("msg", "")
    if e.get("job"):
        key += " [%s]" % e["job"]
    if e.get("err"):
        key += ": " + str(e["err"])[:90]
    key = re.sub(r"//[^/@ ]+@", "//***@", key)
    key = re.sub(r"(?i)(password|token|key)=[^ &\"]+", r"\1=***", key)
    key = re.sub(r"[0-9]+", "N", key)
    t = str(e.get("time", ""))  # 2026-10-02T05:12:00.1Z
    when = t[5:10] + " " + t[11:16] if len(t) >= 16 else ""
    c = seen.setdefault(key, [0, 0, ""])
    c[0] += 1
    if start and when >= start:
        c[1] += 1
    c[2] = max(c[2], when)
for key, (n, after, last) in sorted(seen.items(), key=lambda kv: -kv[1][0])[:4]:
    print("      %5d, %d since the start, last %s  %s" % (n, after, last or "?", key))
PY
        fi
    done

spool() {
    local d=$1 n oldest
    [ -d "$d" ] || return 0
    n=$(find "$d" -type f | wc -l)
    oldest=$(find "$d" -type f -printf '%T@\n' | sort -n | head -1)
    printf '  %-26s %6d files, %6s, oldest %s min\n' "$d" "$n" "$(du -sh "$d" | cut -f1)" \
        "$([ -n "$oldest" ] && echo $(( ($(date +%s) - ${oldest%.*}) / 60 )) || echo -)"
}

case "$role" in
worker | standby)
    echo "-- spools (files waiting to go to the archive; the current minute's file is normal)"
    spool /var/lib/tracks/spool
    spool /var/lib/tracks/walk-spool
    exit 0
    ;;
data) ;;
*) exit 0 ;;
esac

echo "-- backups"
runuser -u postgres -- pgbackrest info 2>/dev/null | grep -E 'status|full backup|diff backup|incr backup|timestamp start' | tail -6 | sed 's/^/  /'

runuser -u postgres -- env PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=300s" \
    psql -d adhunters -X -q -v ON_ERROR_STOP=0 -P pager=off -P footer=off <<'SQL'
SET TIME ZONE 'UTC';
\echo
\echo '== Database'
SELECT pg_size_pretty(pg_database_size(current_database())) AS size,
       (SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()) AS connections,
       (SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'idle in transaction') AS idle_in_transaction,
       (SELECT coalesce(max(extract(epoch FROM now() - xact_start))::int, 0) FROM pg_stat_activity
        WHERE datname = current_database() AND state <> 'idle' AND pid <> pg_backend_pid()) AS longest_transaction_s;

\echo
\echo '== Loader: raw capture files'
SELECT count(*) FILTER (WHERE loaded_at IS NULL AND quarantined_at IS NULL) AS waiting,
       count(*) FILTER (WHERE loaded_at IS NULL AND quarantined_at IS NOT NULL) AS set_aside,
       to_char(min(minute) FILTER (WHERE loaded_at IS NULL AND quarantined_at IS NULL), 'MM-DD HH24:MI') AS oldest_waiting,
       to_char(max(archived_at), 'MM-DD HH24:MI') AS last_archived,
       to_char(max(loaded_at), 'MM-DD HH24:MI') AS last_loaded,
       count(*) FILTER (WHERE loaded_at > now() - interval '24 hours' AND scrapes IS DISTINCT FROM rows) AS unbalanced_24h
FROM tracks.raw_file;
SELECT to_char(max(hour) FILTER (WHERE closed_at IS NOT NULL AND NOT dirty), 'MM-DD HH24:00') AS last_closed_hour,
       count(*) FILTER (WHERE dirty) AS dirty_hours,
       count(*) FILTER (WHERE dirty AND hour < date_trunc('hour', now()) - interval '2 hours') AS dirty_older_than_2h,
       round(avg(took_ms) FILTER (WHERE closed_at > now() - interval '24 hours')) AS close_ms_avg
FROM tracks.hour_state;

\echo
\echo '== Capture per hour (Tracks own scrapes; minutes with a raw file per capture instance)'
WITH s AS (
    SELECT date_trunc('hour', at) AS h, count(*) AS n,
           count(*) FILTER (WHERE outcome IN ('ok', 'empty')) AS answered,
           count(*) FILTER (WHERE outcome = 'empty') AS empty,
           sum(ad_count) FILTER (WHERE outcome IN ('ok', 'empty')) AS ads,
           count(DISTINCT proxy_line_id) AS lines
    FROM tracks.scrape WHERE at >= date_trunc('hour', now()) - interval '30 hours' GROUP BY 1),
f AS (
    SELECT h, string_agg(instance || ':' || m, ' ' ORDER BY instance) AS minutes
    FROM (SELECT date_trunc('hour', minute) AS h, instance, count(DISTINCT date_trunc('minute', minute)) AS m
          FROM tracks.raw_file WHERE minute >= date_trunc('hour', now()) - interval '30 hours' GROUP BY 1, 2) x
    GROUP BY h)
SELECT to_char(s.h, 'MM-DD HH24') AS hour, s.n AS scrapes, s.answered, s.n - s.answered AS failed,
       round(100.0 * (s.n - s.answered) / nullif(s.n, 0), 1) AS failed_pct, s.empty,
       round(s.ads::numeric / nullif(s.answered, 0), 1) AS ads_per_answered, s.lines, f.minutes
FROM s LEFT JOIN f ON f.h = s.h ORDER BY s.h;

\echo
\echo '== Capture per proxy line, last 24 h'
SELECT coalesce(l.code, '-') AS line, count(*) AS scrapes,
       round(100.0 * count(*) FILTER (WHERE outcome NOT IN ('ok', 'empty')) / count(*), 1) AS failed_pct,
       count(*) FILTER (WHERE outcome = 'http') AS http, count(*) FILTER (WHERE outcome = 'error') AS error,
       count(*) FILTER (WHERE outcome = 'unparsed') AS unparsed,
       round(avg(ad_count) FILTER (WHERE outcome IN ('ok', 'empty')), 1) AS ads_per_answered,
       percentile_disc(0.5) WITHIN GROUP (ORDER BY latency_ms) AS median_ms
FROM tracks.scrape s LEFT JOIN tracks.proxy_line l ON l.id = s.proxy_line_id
WHERE at >= now() - interval '24 hours' GROUP BY 1 ORDER BY 1;

\echo '== Capture per instance, last 24 h'
SELECT instance, count(*) AS scrapes,
       round(100.0 * count(*) FILTER (WHERE outcome NOT IN ('ok', 'empty')) / count(*), 1) AS failed_pct,
       string_agg(DISTINCT version, ' ') AS versions, to_char(max(at), 'MM-DD HH24:MI') AS last_scrape
FROM tracks.scrape WHERE at >= now() - interval '24 hours' GROUP BY 1 ORDER BY 1;

\echo '== Failed scrapes by reason, last 24 h'
SELECT outcome, coalesce(status::text, '') AS status,
       left(regexp_replace(regexp_replace(coalesce(error, ''), '//[^/@ ]+@', '//***@', 'g'), '[0-9]+', 'N', 'g'), 100) AS error,
       count(*)
FROM tracks.scrape WHERE at >= now() - interval '24 hours' AND outcome NOT IN ('ok', 'empty')
GROUP BY 1, 2, 3 ORDER BY 4 DESC LIMIT 12;

\echo '== Publishers, last 24 h: pages scraped, answered, and the ones failing more than 20%'
SELECT n.code AS network, count(DISTINCT (s.publisher_id, s.device_id)) AS pages_scraped,
       count(DISTINCT (s.publisher_id, s.device_id)) FILTER (WHERE s.outcome IN ('ok', 'empty')) AS pages_answered
FROM tracks.scrape s JOIN tracks.publisher p ON p.id = s.publisher_id JOIN tracks.network n ON n.id = p.network_id
WHERE s.at >= now() - interval '24 hours' GROUP BY 1 ORDER BY 1;
SELECT n.code AS network, p.name AS publisher, d.code AS device, count(*) AS scrapes,
       count(*) FILTER (WHERE s.outcome NOT IN ('ok', 'empty')) AS failed,
       round(100.0 * count(*) FILTER (WHERE s.outcome NOT IN ('ok', 'empty')) / count(*), 1) AS failed_pct
FROM tracks.scrape s JOIN tracks.publisher p ON p.id = s.publisher_id JOIN tracks.network n ON n.id = p.network_id
JOIN tracks.device d ON d.id = s.device_id
WHERE s.at >= now() - interval '24 hours' GROUP BY 1, 2, 3
HAVING count(*) FILTER (WHERE s.outcome NOT IN ('ok', 'empty')) > 0.2 * count(*)
ORDER BY failed DESC LIMIT 12;

\echo
\echo '== Walker per hour (walks without a line are the old collector''s walker, imported)'
SELECT to_char(date_trunc('hour', at), 'MM-DD HH24') AS hour, count(*) FILTER (WHERE line IS NULL) AS old_walker,
       count(*) FILTER (WHERE line IS NOT NULL) AS tracks_walks,
       count(*) FILTER (WHERE line IS NOT NULL AND outcome = 'ok') AS ok,
       count(*) FILTER (WHERE outcome = 'http_error') AS http_error, count(*) FILTER (WHERE outcome = 'error') AS error,
       round(percentile_disc(0.5) WITHIN GROUP (ORDER BY ms) FILTER (WHERE line IS NOT NULL) / 1000.0, 1) AS median_s
FROM tracks.walk WHERE at >= date_trunc('hour', now()) - interval '30 hours' GROUP BY 1 ORDER BY 1;
\echo '== Walker per day: landing pages read whole (tracks-walker only)'
SELECT w.at::date AS day, count(*) FILTER (WHERE w.line IS NULL) AS old_walker, count(*) FILTER (WHERE w.line IS NOT NULL) AS tracks_walks,
       round(100.0 * count(*) FILTER (WHERE w.line IS NOT NULL AND w.outcome = 'ok') / nullif(count(*) FILTER (WHERE w.line IS NOT NULL), 0), 1) AS ok_pct,
       round(100.0 * count(p.version_hash) FILTER (WHERE w.line IS NOT NULL) / nullif(count(*) FILTER (WHERE w.line IS NOT NULL), 0), 1) AS landing_read_pct
FROM tracks.walk w LEFT JOIN tracks.walk_step p ON p.walk_id = w.id AND p.step = 0
WHERE w.at >= current_date - 6 GROUP BY 1 ORDER BY 1;
\echo '== Walker backlog: ads seen in the last hour with a link, and when they were last walked'
WITH s AS (SELECT DISTINCT ad_id FROM tracks.sighting WHERE seen_at > now() - interval '1 hour' AND link_id IS NOT NULL)
SELECT count(*) AS live_ads, count(*) FILTER (WHERE w.ad_id IS NULL) AS never_walked,
       count(*) FILTER (WHERE w.next_at <= now()) AS due_again,
       round(extract(epoch FROM percentile_disc(0.5) WITHIN GROUP (ORDER BY now() - w.next_at) FILTER (WHERE w.next_at <= now())) / 60) AS median_overdue_min,
       round(extract(epoch FROM max(now() - w.next_at) FILTER (WHERE w.next_at <= now())) / 60) AS max_overdue_min,
       round(extract(epoch FROM percentile_disc(0.5) WITHIN GROUP (ORDER BY now() - w.walked_at)) / 60) AS median_since_walked_min
FROM s LEFT JOIN tracks.walk_state w ON w.ad_id = s.ad_id;
\echo '== New ads (first seen 1 to 7 hours ago): walked yet, and how soon'
SELECT count(*) AS new_ads, count(f.at) AS walked,
       round(100.0 * count(f.at) / nullif(count(*), 0), 1) AS walked_pct,
       round(extract(epoch FROM percentile_disc(0.5) WITHIN GROUP (ORDER BY f.at - a.first_seen_at)) / 60) AS median_wait_min,
       round(extract(epoch FROM percentile_disc(0.9) WITHIN GROUP (ORDER BY f.at - a.first_seen_at)) / 60) AS p90_wait_min
FROM tracks.ad a LEFT JOIN LATERAL (SELECT min(w.at) AS at FROM tracks.walk w WHERE w.ad_id = a.id) f ON true
WHERE a.first_seen_at >= now() - interval '7 hours' AND a.first_seen_at < now() - interval '1 hour';
\echo '== Walker per line, last 24 h'
SELECT coalesce(line, '-') AS line, count(*) AS walks, round(100.0 * count(*) FILTER (WHERE outcome = 'ok') / count(*), 1) AS ok_pct
FROM tracks.walk WHERE at >= now() - interval '24 hours' GROUP BY 1 ORDER BY 1;
\echo '== Walk errors, last 24 h'
SELECT outcome, left(regexp_replace(regexp_replace(coalesce(error, ''), '//[^/@ ]+@', '//***@', 'g'), '[0-9]+', 'N', 'g'), 100) AS error, count(*)
FROM tracks.walk WHERE at >= now() - interval '24 hours' AND outcome <> 'ok' GROUP BY 1, 2 ORDER BY 3 DESC LIMIT 8;
\echo '== Walk files archived (raw walk records)'
SELECT to_char(max(minute), 'MM-DD HH24:MI') AS last_file_minute, to_char(max(archived_at), 'MM-DD HH24:MI') AS last_archived,
       count(*) FILTER (WHERE minute >= now() - interval '24 hours') AS files_24h,
       count(DISTINCT date_trunc('minute', minute)) FILTER (WHERE minute >= now() - interval '24 hours') AS minutes_24h
FROM tracks.walk_file;

\echo
\echo '== Raposa: investigations asked for in the last 24 h'
SELECT mode, origin, status, count(*) FROM raposa.investigation
WHERE requested_at >= now() - interval '24 hours' GROUP BY 1, 2, 3 ORDER BY 1, 2, 3;
\echo '== Raposa: waiting and running now'
SELECT mode, count(*) FILTER (WHERE status = 'waiting') AS waiting, count(*) FILTER (WHERE status = 'running') AS running,
       to_char(min(requested_at) FILTER (WHERE status = 'waiting'), 'MM-DD HH24:MI') AS oldest_waiting,
       count(*) FILTER (WHERE status = 'running' AND claimed_until < now() - interval '10 minutes') AS running_unclaimed
FROM raposa.investigation WHERE status IN ('waiting', 'running') GROUP BY 1 ORDER BY 1;
\echo '== Raposa: finished in the last 24 h'
SELECT mode, count(*) AS finished, count(*) FILTER (WHERE status = 'completed') AS completed,
       count(*) FILTER (WHERE status = 'failed') AS failed, count(*) FILTER (WHERE is_cloaked) AS cloaked,
       round((percentile_disc(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM completed_at - started_at)) / 60)::numeric, 1) AS median_min
FROM raposa.investigation WHERE completed_at >= now() - interval '24 hours' GROUP BY 1 ORDER BY 1;
\echo '== Raposa: visits per line, last 24 h (quick runs never use res-1)'
SELECT i.mode, coalesce(nullif(v.line_key, ''), '-') AS line, count(*) AS visits,
       count(*) FILTER (WHERE v.outcome = 'error') AS errors, round(sum(v.bytes_used) / 1e6, 1) AS mb
FROM raposa.visit v JOIN raposa.investigation i ON i.id = v.investigation_id
WHERE v.started_at >= now() - interval '24 hours' GROUP BY 1, 2 ORDER BY 1, 2;
SELECT left(regexp_replace(regexp_replace(coalesce(error, ''), '//[^/@ ]+@', '//***@', 'g'), '[0-9]+', 'N', 'g'), 100) AS visit_error, count(*)
FROM raposa.visit WHERE started_at >= now() - interval '24 hours' AND outcome = 'error' GROUP BY 1 ORDER BY 2 DESC LIMIT 6;
SELECT key, value FROM raposa.setting WHERE key LIKE 'quick%' ORDER BY key;

\echo
\echo '== Spy: last refreshes'
SELECT to_char((SELECT max(refreshed_at) FROM spy.creative_stats), 'MM-DD HH24:MI') AS creative_stats,
       to_char((SELECT window_end FROM spy.recent_window), 'MM-DD HH24:MI') AS last_24h_ends,
       to_char((SELECT refreshed_at FROM spy.recent_window), 'MM-DD HH24:MI') AS last_24h_refreshed,
       to_char((SELECT max(refreshed_at) FROM spy.direction_stats), 'MM-DD HH24:MI') AS direction,
       to_char((SELECT max(trained_at) FROM spy.class_model), 'MM-DD HH24:MI') AS model_trained;

-- ---- Tracks against the old collector ----------------------------------------
-- Tracks' last closed hours (up to 24, all after the switch), and the same
-- clock hours on each of the 7 days before. Old days are the collector's own
-- counts (import-old), which stored answered scrapes only.
SELECT to_char(greatest(f.first, c.last - interval '23 hours'), 'YYYY-MM-DD"T"HH24:00:00Z') AS t0,
       to_char(c.last + interval '1 hour', 'YYYY-MM-DD"T"HH24:00:00Z') AS t1
FROM (SELECT max(hour) + interval '1 hour' AS first FROM tracks.hour_state WHERE imported_at IS NOT NULL) f,
     (SELECT max(hour) AS last FROM tracks.hour_state WHERE closed_at IS NOT NULL AND NOT dirty AND imported_at IS NULL) c
\gset
\echo
\echo '== Tracks against the old collector: the same clock hours, from' :t0 'to' :t1
WITH w AS (
    SELECT k, :'t0'::timestamptz - k * interval '1 day' AS a, :'t1'::timestamptz - k * interval '1 day' AS b
    FROM generate_series(0, 7) k),
who AS (
    SELECT w.k, CASE WHEN bool_and(h.imported_at IS NOT NULL) THEN 'old collector'
                     WHEN bool_and(h.imported_at IS NULL) THEN 'Tracks' ELSE 'mixed' END AS who
    FROM w JOIN tracks.hour_state h ON h.hour >= w.a AND h.hour < w.b GROUP BY w.k),
ph AS (
    SELECT w.k, n.code AS network, sum(x.answered) AS answered, sum(x.failed) AS failed, sum(x.sightings) AS sightings,
           count(*) FILTER (WHERE x.answered > 0) AS page_hours
    FROM w JOIN tracks.publisher_hourly x ON x.hour >= w.a AND x.hour < w.b
    JOIN tracks.publisher p ON p.id = x.publisher_id JOIN tracks.network n ON n.id = p.network_id
    GROUP BY GROUPING SETS ((w.k, n.code), (w.k))),
pages AS (
    SELECT w.k, n.code AS network, count(DISTINCT (x.publisher_id, x.device_id)) AS pages
    FROM w JOIN tracks.publisher_hourly x ON x.hour >= w.a AND x.hour < w.b AND x.answered > 0
    JOIN tracks.publisher p ON p.id = x.publisher_id JOIN tracks.network n ON n.id = p.network_id
    GROUP BY GROUPING SETS ((w.k, n.code), (w.k))),
ah AS (
    SELECT w.k, n.code AS network, count(DISTINCT x.ad_id) AS ads, count(DISTINCT a.creative_id) AS creatives
    FROM w JOIN tracks.ad_hourly x ON x.hour >= w.a AND x.hour < w.b JOIN tracks.ad a ON a.id = x.ad_id
    JOIN tracks.publisher p ON p.id = x.publisher_id JOIN tracks.network n ON n.id = p.network_id
    GROUP BY GROUPING SETS ((w.k, n.code), (w.k)))
SELECT to_char(w.a, 'MM-DD') AS day, who.who, coalesce(ph.network, 'all') AS network, pages.pages,
       ph.answered AS answered_scrapes, ph.failed, ph.sightings,
       round(ph.sightings::numeric / nullif(ph.answered, 0), 1) AS sightings_per_scrape, ah.ads, ah.creatives
FROM w JOIN who ON who.k = w.k JOIN ph ON ph.k = w.k
LEFT JOIN pages ON pages.k = w.k AND pages.network IS NOT DISTINCT FROM ph.network
LEFT JOIN ah ON ah.k = w.k AND ah.network IS NOT DISTINCT FROM ph.network
ORDER BY ph.network NULLS FIRST, w.k;

\echo '== Landing page walks in those hours: the old collector''s walker against tracks-walker'
WITH w AS (
    SELECT k, :'t0'::timestamptz - k * interval '1 day' AS a, :'t1'::timestamptz - k * interval '1 day' AS b
    FROM generate_series(0, 3) k)
SELECT to_char(w.a, 'MM-DD') AS day, CASE WHEN x.line IS NULL THEN 'old walker' ELSE 'tracks-walker' END AS walker,
       count(*) AS walks, count(*) FILTER (WHERE x.outcome = 'ok') AS ok, count(DISTINCT x.ad_id) AS ads,
       count(DISTINCT x.creative_id) AS creatives, count(DISTINCT x.link_id) AS links
FROM w JOIN tracks.walk x ON x.at >= w.a AND x.at < w.b
GROUP BY w.k, w.a, 2 ORDER BY w.k, 2;

\echo '== Which creatives each saw in those hours'
WITH w AS (
    SELECT k, :'t0'::timestamptz - k * interval '1 day' AS a, :'t1'::timestamptz - k * interval '1 day' AS b
    FROM generate_series(0, 2) k),
c AS MATERIALIZED (
    SELECT w.k, a.creative_id, sum(x.sightings) AS s
    FROM w JOIN tracks.ad_hourly x ON x.hour >= w.a AND x.hour < w.b JOIN tracks.ad a ON a.id = x.ad_id GROUP BY 1, 2),
top AS (SELECT k, creative_id, row_number() OVER (PARTITION BY k ORDER BY s DESC) AS r FROM c),
pairs (x, y, label) AS (VALUES (0, 1, 'today, of what the day before saw'), (1, 2, 'the day before, of the day before it (usual churn)'))
SELECT p.label,
       (SELECT count(*) FROM c WHERE c.k = p.y) AS their_creatives,
       (SELECT count(*) FROM c cy JOIN c cx ON cx.creative_id = cy.creative_id AND cx.k = p.x WHERE cy.k = p.y) AS also_seen,
       round(100.0 * (SELECT sum(cy.s) FROM c cy JOIN c cx ON cx.creative_id = cy.creative_id AND cx.k = p.x WHERE cy.k = p.y)
             / nullif((SELECT sum(s) FROM c WHERE c.k = p.y), 0), 1) AS by_sightings_pct,
       (SELECT count(*) FROM top t JOIN c cx ON cx.creative_id = t.creative_id AND cx.k = p.x WHERE t.k = p.y AND t.r <= 500) AS of_top_500,
       (SELECT count(*) FROM c cx WHERE cx.k = p.x
          AND NOT EXISTS (SELECT 1 FROM c cy WHERE cy.k = p.y AND cy.creative_id = cx.creative_id)) AS only_the_later_day
FROM pairs p ORDER BY p.x;

\echo '== Pages (publisher and device) answered the day before but not today, or only today'
WITH y AS (
    SELECT publisher_id, device_id, sum(answered) AS n, sum(sightings) AS s FROM tracks.publisher_hourly
    WHERE hour >= :'t0'::timestamptz - interval '1 day' AND hour < :'t1'::timestamptz - interval '1 day'
    GROUP BY 1, 2 HAVING sum(answered) > 0),
t AS (
    SELECT publisher_id, device_id, sum(answered) AS n, sum(failed) AS failed, sum(sightings) AS s FROM tracks.publisher_hourly
    WHERE hour >= :'t0'::timestamptz AND hour < :'t1'::timestamptz GROUP BY 1, 2)
SELECT CASE WHEN coalesce(t.n, 0) = 0 THEN 'day before only' ELSE 'today only' END AS side, nw.code AS network,
       p.name AS publisher, d.code AS device, coalesce(y.n, 0) AS before_scrapes, coalesce(t.n, 0) AS today_answered,
       coalesce(t.failed, 0) AS today_failed
FROM y FULL JOIN t ON t.publisher_id = y.publisher_id AND t.device_id = y.device_id
JOIN tracks.publisher p ON p.id = coalesce(y.publisher_id, t.publisher_id) JOIN tracks.network nw ON nw.id = p.network_id
JOIN tracks.device d ON d.id = coalesce(y.device_id, t.device_id)
WHERE (y.n > 0 AND coalesce(t.n, 0) = 0) OR (t.n > 0 AND y.n IS NULL)
ORDER BY side, coalesce(y.n, 0) + coalesce(t.n, 0) DESC LIMIT 25;

\echo '== Pages whose scrapes or ads per scrape moved most (30+ scrapes on both days)'
WITH y AS (
    SELECT publisher_id, device_id, sum(answered) AS n, sum(sightings) AS s FROM tracks.publisher_hourly
    WHERE hour >= :'t0'::timestamptz - interval '1 day' AND hour < :'t1'::timestamptz - interval '1 day' GROUP BY 1, 2),
t AS (
    SELECT publisher_id, device_id, sum(answered) AS n, sum(sightings) AS s FROM tracks.publisher_hourly
    WHERE hour >= :'t0'::timestamptz AND hour < :'t1'::timestamptz GROUP BY 1, 2)
SELECT nw.code AS network, p.name AS publisher, d.code AS device, y.n AS before_scrapes, t.n AS today_scrapes,
       round(y.s::numeric / y.n, 1) AS before_ads_per_scrape, round(t.s::numeric / t.n, 1) AS today_ads_per_scrape
FROM y JOIN t USING (publisher_id, device_id)
JOIN tracks.publisher p ON p.id = y.publisher_id JOIN tracks.network nw ON nw.id = p.network_id JOIN tracks.device d ON d.id = y.device_id
WHERE y.n >= 30 AND t.n >= 30
  AND (abs(ln((t.s::numeric / t.n + 0.1) / (y.s::numeric / y.n + 0.1))) > ln(1.25) OR abs(ln(t.n::numeric / y.n)) > ln(2))
ORDER BY abs(ln((t.s::numeric / t.n + 0.1) / (y.s::numeric / y.n + 0.1))) + abs(ln(t.n::numeric / y.n)) DESC LIMIT 15;

\echo '== Per hour across the switch (answered scrapes and sightings)'
SELECT to_char(x.hour, 'MM-DD HH24') AS hour,
       CASE WHEN h.imported_at IS NOT NULL THEN 'old collector' ELSE 'Tracks' END AS who,
       sum(x.answered) AS answered, sum(x.failed) AS failed, sum(x.sightings) AS sightings,
       round(sum(x.sightings)::numeric / nullif(sum(x.answered), 0), 1) AS per_scrape
FROM tracks.publisher_hourly x JOIN tracks.hour_state h ON h.hour = x.hour
WHERE x.hour >= :'t1'::timestamptz - interval '36 hours' AND x.hour < :'t1'::timestamptz
GROUP BY x.hour, h.imported_at ORDER BY x.hour;
SQL

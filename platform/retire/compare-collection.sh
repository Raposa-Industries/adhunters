#!/usr/bin/env bash
# Does Tracks collect as much as the old collector? Compares one UTC day of
# both, side by side: scrapes per hour and per proxy line, ads per scrape per
# publisher and device, and which creatives each saw. It only reads: Tracks on
# the data box, and the collector's database on prodbox through the data box's
# OLD_DATABASE_URL (as save-old-data.sh finds it). Run it on your own computer
# (Tailscale up), from the repository:
#
#   platform/retire/compare-collection.sh [day] [old collector's day]
#
# The day defaults to yesterday (UTC). Tracks keeps sightings 3 days, so it must
# be one of the last three. After the switch-over the old collector no longer
# scrapes, so give its last full day as the second argument (2026-10-01):
# Tracks' day is then compared with that one.
# Avoid 00:00-00:15 and 04:00-04:30 UTC, when the collector adds and drops
# its sighting partitions.
set -euo pipefail

day=${1:-$(date -u -d yesterday +%F)}
next=$(date -u -d "$day + 1 day" +%F)
old_day=${2:-$day}
old_next=$(date -u -d "$old_day + 1 day" +%F)
data=admin@adhunters-data
ssh_opts=(-o ConnectTimeout=15 -o BatchMode=yes)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

f=$(ssh "${ssh_opts[@]}" "$data" 'for f in /etc/adhunters/tracks-bridge.env /etc/adhunters/spy-numbers.env; do
    sudo grep -q "^OLD_DATABASE_URL=postgres" "$f" 2>/dev/null && { echo "$f"; exit 0; }; done; exit 1') ||
    { echo "no OLD_DATABASE_URL on the data box (platform/retire/README.md)" >&2; exit 1; }

old() { ssh "${ssh_opts[@]}" "$data" "sudo bash -c 'set -a; . $f; set +a; PGOPTIONS=\"-c default_transaction_read_only=on -c statement_timeout=0\" psql \"\$OLD_DATABASE_URL\" -X -q --csv -v ON_ERROR_STOP=1 -v d0=\"$old_day 00:00Z\" -v d1=\"$old_next 00:00Z\"'"; }
new() { ssh "${ssh_opts[@]}" "$data" "sudo -u postgres psql -d adhunters -X -q --csv -v ON_ERROR_STOP=1 -v d0='$day 00:00Z' -v d1='$next 00:00Z'"; }

echo "== $old_day: reading the old collector (prodbox)"
old > "$work/old-lines.csv" <<'SQL'
SELECT to_char(date_trunc('hour', s.scraped_at AT TIME ZONE 'UTC'), 'HH24') AS hour, coalesce(l.code, '-') AS line,
       'ok' AS outcome, count(*) AS scrapes, sum(s.ad_count) AS ads
FROM spy.scrape s LEFT JOIN spy.proxy_line l ON l.id = s.proxy_line_id
WHERE s.scraped_at >= :'d0' AND s.scraped_at < :'d1' AND (s.worker_node IS NULL OR s.worker_node NOT LIKE 'tracks:%') GROUP BY 1, 2;
SQL
old > "$work/old-pubs.csv" <<'SQL'
SELECT p.name AS publisher, d.code AS device, 'ok' AS outcome, count(*) AS scrapes, sum(s.ad_count) AS ads
FROM spy.scrape s JOIN spy.publisher p ON p.id = s.publisher_id JOIN spy.device d ON d.id = s.device_id
WHERE s.scraped_at >= :'d0' AND s.scraped_at < :'d1' AND (s.worker_node IS NULL OR s.worker_node NOT LIKE 'tracks:%') GROUP BY 1, 2;
SQL
old > "$work/old-creatives.csv" <<'SQL'
SELECT c.creative_key, count(*) AS sightings
FROM spy.sighting s JOIN spy.creative c ON c.id = s.creative_id
WHERE s.seen_at >= :'d0' AND s.seen_at < :'d1' GROUP BY 1;
SQL

echo "== $day: reading Tracks (data box)"
new > "$work/new-lines.csv" <<'SQL'
SELECT to_char(date_trunc('hour', s.at AT TIME ZONE 'UTC'), 'HH24') AS hour, coalesce(l.code, '-') AS line,
       s.outcome, count(*) AS scrapes, sum(s.ad_count) AS ads
FROM tracks.scrape s LEFT JOIN tracks.proxy_line l ON l.id = s.proxy_line_id
WHERE s.at >= :'d0' AND s.at < :'d1' GROUP BY 1, 2, 3;
SQL
new > "$work/new-pubs.csv" <<'SQL'
SELECT p.name AS publisher, d.code AS device, s.outcome, count(*) AS scrapes, sum(s.ad_count) AS ads
FROM tracks.scrape s JOIN tracks.publisher p ON p.id = s.publisher_id JOIN tracks.device d ON d.id = s.device_id
WHERE s.at >= :'d0' AND s.at < :'d1' GROUP BY 1, 2, 3;
SQL
new > "$work/new-creatives.csv" <<'SQL'
SELECT c.creative_key, count(*) AS sightings
FROM tracks.sighting s JOIN tracks.creative c ON c.id = s.creative_id
WHERE s.seen_at >= :'d0' AND s.seen_at < :'d1' GROUP BY 1;
SQL

workers=$(ssh "${ssh_opts[@]}" admin@adhunters-worker 'for u in a b; do systemctl is-active -q tracks-capture@$u && sudo sed -n "s/^WORKERS=//p" /etc/adhunters/tracks-capture@$u.env; done' 2>/dev/null | awk '{n += $1} END {print n ? n : "?"}')
sweepers=$(ssh "${ssh_opts[@]}" bigworker "sed -n 's/^SWEEPER_CONCURRENCY=//p' /opt/adhunters-collector/secrets/collector.env" 2>/dev/null || true)

python3 - "$work" "$day" "${workers:-?}" "${sweepers:-default}" "$old_day" <<'PY'
import csv, math, os, sys, statistics, collections
work, day, workers, sweepers, old_day = sys.argv[1:6]
def load(name):
    return list(csv.DictReader(open(os.path.join(work, name))))
ANSWERED = {"ok", "empty"}

def lines(rows):
    by_line, by_hour = collections.defaultdict(lambda: [0, 0, 0]), collections.Counter()
    failed = 0
    for r in rows:
        n, ads = int(r["scrapes"]), int(r["ads"] or 0)
        if r["outcome"] in ANSWERED:
            by_line[r["line"]][0] += n; by_line[r["line"]][1] += ads; by_hour[r["hour"]] += n
        else:
            by_line[r["line"]][2] += n; failed += n
    return by_line, by_hour, failed

def pubs(rows):
    d = collections.defaultdict(lambda: [0, 0, 0])
    for r in rows:
        k = (r["publisher"].lower(), r["device"])
        if r["outcome"] in ANSWERED:
            d[k][0] += int(r["scrapes"]); d[k][1] += int(r["ads"] or 0)
        else:
            d[k][2] += int(r["scrapes"])
    return d

ol, oh, _ = lines(load("old-lines.csv"))
nl, nh, nfail = lines(load("new-lines.csv"))
op, np_ = pubs(load("old-pubs.csv")), pubs(load("new-pubs.csv"))
oc = {r["creative_key"]: int(r["sightings"]) for r in load("old-creatives.csv")}
nc = {r["creative_key"]: int(r["sightings"]) for r in load("new-creatives.csv")}

os_, oa = sum(v[0] for v in ol.values()), sum(v[1] for v in ol.values())
ns, na = sum(v[0] for v in nl.values()), sum(v[1] for v in nl.values())
f = ns / os_ if os_ else 0
pct = lambda a, b: "%.1f%%" % (100.0 * a / b) if b else "-"
per = lambda a, b: "%.2f" % (a / b) if b else "-"

if old_day == day:
    print("\n# %s (UTC): Tracks vs the old collector\n" % day)
else:
    print("\n# Tracks on %s vs the old collector on %s (UTC)\n" % (day, old_day))
print("%-34s %14s %14s" % ("", "old collector", "Tracks"))
print("%-34s %14s %14s" % ("capture workers / sweepers", sweepers, workers))
print("%-34s %14d %14d" % ("proxy lines used", len([k for k in ol if k != "-"]), len([k for k in nl if k != "-"])))
print("%-34s %14d %14d" % ("scrapes answered", os_, ns))
print("%-34s %14s %14s" % ("scrapes failed", "not stored", "%d (%s)" % (nfail, pct(nfail, ns + nfail))))
print("%-34s %14d %14d" % ("ads seen (sightings)", oa, na))
print("%-34s %14s %14s" % ("ads per answered scrape", per(oa, os_), per(na, ns)))
print("%-34s %14d %14d" % ("distinct creatives", len(oc), len(nc)))
print("\nTracks made %s of the old collector's scrapes." % pct(ns, os_))

print("\n## Per proxy line (answered scrapes per hour, ads per scrape)")
for name, d in (("old", ol), ("Tracks", nl)):
    for k in sorted(d):
        s, a, fl = d[k]
        print("  %-7s %-10s %8.0f/h  %s ads/scrape%s" % (name, k, s / 24.0, per(a, s), ("  failed %d" % fl) if fl else ""))
nlines = len([k for k in nl if k != "-"]) or 1
try:
    w = float(workers)
    print("\nTracks: %.0f answered scrapes/h per capture worker. Matching the old %.0f/h needs about %.0f workers." %
          (ns / 24.0 / w, os_ / 24.0, math.ceil((os_ / 24.0) / (ns / 24.0 / w))) if ns else "")
except ValueError:
    pass

print("\n## Hours (answered scrapes)")
med = statistics.median([nh.get("%02d" % h, 0) for h in range(24)]) or 1
gaps = [("%02d" % h, nh.get("%02d" % h, 0)) for h in range(24) if nh.get("%02d" % h, 0) < 0.5 * med]
print("  Tracks median %.0f/h; hours under half of that: %s" % (med, ", ".join("%s:00 (%d)" % g for g in gaps) or "none"))
omed = statistics.median([oh.get("%02d" % h, 0) for h in range(24)]) or 1
ogaps = [("%02d" % h, oh.get("%02d" % h, 0)) for h in range(24) if oh.get("%02d" % h, 0) < 0.5 * omed]
print("  old median %.0f/h; hours under half of that: %s" % (omed, ", ".join("%s:00 (%d)" % g for g in ogaps) or "none"))

print("\n## Publishers and devices")
missing = [k for k in op if k not in np_ or np_[k][0] == 0]
print("  scraped by the old collector, never answered for Tracks: %d of %d" % (len(missing), len(op)))
for k in sorted(missing, key=lambda k: -op[k][0])[:15]:
    extra = (", Tracks failed %d" % np_[k][2]) if k in np_ and np_[k][2] else ""
    print("    %s %s: old %d scrapes%s" % (k[0], k[1], op[k][0], extra))
only_new = [k for k in np_ if k not in op and np_[k][0]]
if only_new:
    print("  answered for Tracks only (a name differs, or a target the collector skips): %s" % ", ".join("%s %s" % k for k in only_new[:15]))
odd = []
for k, (s, a, _) in op.items():
    if k in np_ and s >= 30 and np_[k][0] >= 30:
        ro, rn = a / s, np_[k][1] / np_[k][0]
        if ro and (rn / ro < 0.8 or rn / ro > 1.25):
            odd.append((k, ro, rn, np_[k][0]))
print("  ads per scrape more than 20%% apart (30+ scrapes each): %d" % len(odd))
for k, ro, rn, s in sorted(odd, key=lambda x: -x[3])[:15]:
    print("    %s %s: old %.2f, Tracks %.2f (%d scrapes)" % (k[0], k[1], ro, rn, s))

print("\n## Creatives")
both = set(oc) & set(nc)
total_o = sum(oc.values()) or 1
print("  the old collector's creatives Tracks also saw: %s (%d of %d)" % (pct(len(both), len(oc)), len(both), len(oc)))
print("  weighted by how often the old collector saw them: %s" % pct(sum(oc[k] for k in both), total_o))
top = sorted(oc, key=lambda k: -oc[k])[:500]
print("  of the old collector's top 500: %s" % pct(len([k for k in top if k in nc]), len(top)))
# If Tracks sampled the same ads at its lower rate, a creative the old
# collector saw k times shows up in Tracks with probability about 1-exp(-k*f).
expect = sum(1 - math.exp(-k * f) for k in oc.values())
print("  expected at Tracks' scrape rate if it saw the same ads: %.0f; it saw %d (%s of expected)" % (expect, len(both), pct(len(both), expect)))
print("  creatives only Tracks saw: %d" % len(set(nc) - set(oc)))
missed = [k for k in top if k not in nc][:10]
if missed:
    print("  most-seen creatives Tracks missed (old sightings):")
    for k in missed:
        print("    %s  %d" % (k[:60], oc[k]))
PY

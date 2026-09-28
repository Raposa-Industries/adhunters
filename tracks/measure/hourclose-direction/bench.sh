#!/usr/bin/env bash
# Times the hour close and Direction on this box, from one archived day of
# sightings. Meant for a fresh, throwaway Ubuntu 24.04 server (a CX43), run as
# root. It installs PostgreSQL 17, makes a database called bench, and writes
# results.md next to itself. It never connects anywhere else.
#
#   ./bench.sh /root/sighting_20260913.tsv.gz
#
# SKIP_INSTALL=1 uses the PostgreSQL already running (for a trial run
# elsewhere); PSQL overrides how psql is called.
set -euo pipefail
cd "$(dirname "$0")"

archive=${1:?usage: bench.sh sighting_YYYYMMDD.tsv.gz}
[[ -r $archive ]] || { echo "cannot read $archive"; exit 1; }
day=$(basename "$archive" | grep -oE '[0-9]{8}' | head -1)
[[ -n $day ]] || { echo "no yyyymmdd in the file name"; exit 1; }
day=${day:0:4}-${day:4:2}-${day:6:2}
history_days=${HISTORY_DAYS:-27}

if [[ ${SKIP_INSTALL:-0} != 1 ]] && ! command -v /usr/lib/postgresql/17/bin/postgres >/dev/null; then
  echo "== installing PostgreSQL 17"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq postgresql-common >/dev/null
  /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y >/dev/null
  apt-get install -y -qq postgresql-17 >/dev/null
  # Sized for a CX43 (8 vCPU, 16 GB): what the data box would run with.
  runuser -u postgres -- psql -qX <<'SQL'
ALTER SYSTEM SET shared_buffers = '4GB';
ALTER SYSTEM SET effective_cache_size = '11GB';
ALTER SYSTEM SET work_mem = '64MB';
ALTER SYSTEM SET maintenance_work_mem = '1GB';
ALTER SYSTEM SET max_wal_size = '8GB';
ALTER SYSTEM SET checkpoint_timeout = '15min';
ALTER SYSTEM SET random_page_cost = 1.1;
ALTER SYSTEM SET effective_io_concurrency = 200;
ALTER SYSTEM SET max_parallel_workers_per_gather = 4;
ALTER SYSTEM SET jit = off;
SQL
  systemctl restart postgresql
fi

PSQL=${PSQL:-runuser -u postgres -- psql}
db() { $PSQL -X -q -v ON_ERROR_STOP=1 -d bench "$@"; }
now_ms() { echo $(( $(date +%s%N) / 1000000 )); }
# timed <step> <detail> <sql>: runs sql in its own session and records how long it took.
timed() {
  local start end
  start=$(now_ms)
  db -c "$3" >/dev/null
  end=$(now_ms)
  db -c "INSERT INTO spy.bench_timing VALUES ('$1', '$2', $((end - start)), NULL)"
  printf '   %-34s %-26s %8d ms\n' "$1" "$2" $((end - start))
}

echo "== fresh database bench, archived day $day"
$PSQL -X -q -c "DROP DATABASE IF EXISTS bench" -c "CREATE DATABASE bench"
$PSQL -X -q -c "ALTER DATABASE bench SET search_path = clock, spy, public, pg_catalog"
db -f 01_schema.sql
db -f 05_direction.sql
db -f 03_hour_close.sql
db -f 04_history.sql
db -c "SELECT spy.bench_day_partition('$day'::date)" \
   -c "SELECT spy.bench_month_partitions('$day'::date - $((history_days + 8)), '$day'::date + 1)" >/dev/null

echo "== loading"
start=$(now_ms)
zcat "$archive" | grep -v '^#' | db -c "\copy spy.sighting_archive FROM STDIN"
end=$(now_ms)
db -c "INSERT INTO spy.bench_timing SELECT 'load.copy_archive', 'COPY of the gzip TSV', $((end - start)), count(*) FROM spy.sighting_archive"
printf '   %-34s %-26s %8d ms\n' load.copy_archive "" $((end - start))
db -c "ANALYZE spy.sighting_archive"
timed load.lookups "made-up lookups" "$(cat 02_load.sql)"
timed load.sightings "into the daily partition" \
  "INSERT INTO spy.sighting SELECT s.*, a.account_id, a.brand_id, NULL FROM spy.sighting_archive s JOIN spy.ad a ON a.id = s.ad_id"
db -c "ANALYZE spy.sighting"

echo "== closing each hour"
for h in $(seq -w 0 23); do
  timed close_hour.total "$day $h:00" "SELECT spy.close_hour('$day $h:00:00+00')"
done
timed close_hour.again "$day 12:00 (re-run)" "SELECT spy.close_hour('$day 12:00:00+00')"
db -c "SELECT spy.close_day('$day')" >/dev/null
db -tA -c "SELECT format('   %-34s %-26s %8s ms', step, detail, round(ms)) FROM spy.bench_timing WHERE step = 'close_day'"

balance=$(db -tA -c "SELECT (SELECT count(*) FROM spy.sighting) - (SELECT sum(sightings) FROM spy.ad_hourly)")
[[ $balance == 0 ]] || { echo "books do not balance: sightings minus hourly counts = $balance"; exit 1; }
echo "   books balance: every sighting is in exactly one hourly count"

echo "== making $history_days days of history for Direction"
timed history "$history_days days" "SELECT spy.bench_history('$day', $history_days)"
db -c "VACUUM ANALYZE"

echo "== Direction (clock set inside the archived day)"
at() { $PSQL -X -q -c "ALTER DATABASE bench SET bench.now = '$day $1:00+00'"; }
at 22:30
timed direction.rebuild "daily part alone" "SELECT spy.direction_rebuild()"
timed direction.size "hourly Size alone" "SELECT spy.refresh_size()"
timed direction.first_run "22:30, rebuild + 8 hours" "SELECT spy.refresh_direction(true)"
at 22:35
timed direction.five_min "22:35" "SELECT spy.refresh_direction()"
at 22:40
timed direction.five_min "22:40" "SELECT spy.refresh_direction()"
at 23:05
timed direction.new_hour "23:05, new hour" "SELECT spy.refresh_direction()"
at 23:10
timed direction.five_min "23:10" "SELECT spy.refresh_direction()"

echo "== writing results.md"
{
  echo "# Hour close and Direction timing"
  echo
  echo "Archived day $day, $(db -tA -c "SELECT to_char(count(*), 'FM999,999,999') FROM spy.sighting") sightings,"
  echo "$(db -tA -c "SELECT to_char(count(*), 'FM999,999,999') FROM spy.ad") ads, $history_days days of made-up history."
  echo "Box: $(nproc) vCPU ($(lscpu | sed -n 's/^Model name: *//p' | head -1)), $(free -g | awk '/^Mem:/ {print $2}') GB RAM, $(db -tA -c 'SHOW server_version' | cut -d' ' -f1)."
  echo
  echo "## Every step"
  echo
  echo "| step | detail | ms | rows |"
  echo "|---|---|---:|---:|"
  db -tA -F '|' -c "SELECT '|' || step, detail, round(ms), coalesce(row_count::text, '') || '|' FROM spy.bench_timing WHERE step NOT LIKE 'close_hour.%' OR step IN ('close_hour.again') ORDER BY ctid"
  echo
  echo "## Hour close, per table over the 24 hours"
  echo
  echo "| table | min ms | median ms | max ms | rows per hour |"
  echo "|---|---:|---:|---:|---:|"
  db -tA -F '|' -c "SELECT '|' || step, round(min(ms)), round(percentile_cont(0.5) WITHIN GROUP (ORDER BY ms)), round(max(ms)), round(avg(row_count)) || '|'
                    FROM spy.bench_timing WHERE step LIKE 'close_hour.%' AND step NOT IN ('close_hour.again') GROUP BY step ORDER BY step"
  echo
  echo "## Sizes"
  echo
  echo "| table | rows | size |"
  echo "|---|---:|---:|"
  for t in sighting ad_hourly ad_account_brand_hourly publisher_hourly ad_daily direction_usual direction_stats size_stats; do
    db -tA -F '|' -c "SELECT '|$t', to_char(count(*), 'FM999,999,999'),
                             pg_size_pretty(coalesce((SELECT sum(pg_total_relation_size(inhrelid)) FROM pg_inherits WHERE inhparent = 'spy.$t'::regclass), 0)
                                            + pg_total_relation_size('spy.$t')) || '|' FROM spy.$t"
  done
  echo
  echo "## Direction's answers after the last run"
  echo
  echo "The history is made up, so these only show how much work the last run had:"
  echo "push signs are computed for rising rows only."
  echo
  echo "| kind | direction | rows |"
  echo "|---|---|---:|"
  db -tA -F '|' -c "SELECT '|' || kind, direction, count(*) || '|' FROM spy.direction_stats GROUP BY 1, 2 ORDER BY 1, 2"
  echo
  echo "Direction on prodbox today takes about 4 minutes a run (design page)."
} > results.md
cat results.md

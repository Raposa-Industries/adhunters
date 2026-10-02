# 0023 · Hourly counts move to the archive after 35 days

**Decided:** 2 Oct 2026, by the owner (35 days, on a choice between 35 and
90), building on decision 0007 and the design page's decision 3 (v3).

Hourly counts (`ad_hourly`, `ad_account_brand_hourly`) stay in the database
for 35 days after their month ends. Every final day older than 7 days is
also written to one Parquet file per table in the archive
(`hourly/<table>/<yyyy>/<mm>/<dd>-v<n>.parquet`, an hour file), read back
and checked hour by hour, value by value, before it is recorded. A month's
hourly partitions are dropped only by `tracks-loader hourly drop`, which
checks every hour of every day of the month against its hour files again
and drops nothing unless all of them match. The first drop of each month is
run by a person.

What stays in the database for good: the daily counts, `publisher_hourly`
(the checks per hour, small) and `hour_state`. Spy reads ranges older than
35 days in whole UTC days already (`hourly_days` in spy/), so its numbers
do not change. A page that needs an archived day hour by hour asks with
`tracks_api.hourly_days_v1(…, true)`; the loader loads the day's hour files
back within a minute or two and drops them again once nobody has asked for
3 days, after the same check. A day a replay closes again is written to a
new hour file and dropped again the same way; older files stay.

**Why:** hourly counts are most of the database's growth (about 490 MB a
day on 10 capture lines, 2 Oct 2026), and nothing reads them hour by hour
past 35 days. A day's hour files take a few percent of its space in the
database, keep every value, and DuckDB or pandas read them as they are. Raw files
stay in the archive for good, so every count can still be rebuilt from
scratch by a replay.

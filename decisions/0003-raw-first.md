# 0003 · Raw first, facts second; counts once per closed hour

**Decided:** 28 Sep 2026, by Marcos.

Capture writes responses to a local spool with no database dependency; the
shipper archives them to object storage; the loader parses them into facts and
can replay any range. Counts are computed once per closed hour, not upserted
per sighting. Hourly counts older than 35 days move to Parquet in object
storage; daily counts stay in Postgres forever. Derived numbers (24 h windows,
Direction, the read model) belong to Scout, not collection.

**Why:** collection must never pause for a deploy or a database restart, and
per-sighting upserts were most of the database's write load.

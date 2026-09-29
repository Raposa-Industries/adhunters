# Switch-over: Tracks scrapes, today's Spy keeps working

How the old collector stops scraping and Tracks takes over, while today's Spy
(app.adhunters.pro, which reads the collector's database on prodbox) keeps
showing new ads and numbers. Every step here touches the running servers, so
**each one is run by the owner, or run only after the owner says yes in
words.** Nothing below has been run yet.

## The approach, and why

Tracks scrapes. A new binary, `tracks-bridge` on the data box, reads every raw
file Tracks archived from the switch-over minute on, parses it with Tracks'
parser and writes it into the collector's database exactly as the collector's
sweeper did (its `SpyWriter`, ported line by line). The collector keeps
running with only its sweeper off (`SWEEPER_CONCURRENCY=0`): its landing page
walker, Raposa, the spy maintenance jobs (read model, Direction) and the
classifier go on as before, on data the bridge writes. Today's Spy changes
nothing. One thing works differently: the old Raposa took fresh click links
from the sweeper; with the sweeper off it scrapes a feed itself for each visit,
which it already did whenever it had no link. The new Raposa takes Tracks'
live links and is not affected.

Chosen over the other two ways:

- **Point today's Spy at the new database.** Spy's queries read `spy.*` tables
  and the collector's read model. Rewriting them is the new Spy's job (PR #12
  builds its numbers on `tracks_api`); doing it in today's Spy means changing
  a live app twice.
- **Keep the collector scraping and copy into Tracks.** That is what we have
  now: it keeps the proxy lines on bigworker and the restart pauses on every
  deploy, which is the pain point.

The bridge is the smallest change to what runs: one environment variable on
the collector, one new table (`spy.walk_queue`, so the walker still gets
links), and one new service that can be stopped at any time. Going back is one
restart. The bridge only exists until the new Spy launches.

**Nothing collected is lost.** Every scrape is still saved raw in the archive
first. In the collector's database each scrape is keyed by its capture id
(`spy.scrape.batch_uid`), so writing a file again stores nothing twice.
`tracks.bridge_file` records what was written; a file that fails 3 times is
quarantined, alerts (`BridgeFileQuarantined`) and waits for a `redo`.

What differs from the collector's own writes: `worker_node` is
`tracks:<instance>` (`tracks:a`, `tracks:b`), `pipeline_version` is
`tracks-bridge <version>`, and only scrapes that parsed (`ok`) are written,
because the collector never stored failed ones. Tracks keeps those.

## Before the night

### 1. Merge and build

Merge this PR, then build as in `platform/OPERATIONS.md` (Build and deploy) and
run `setup.sh` on the **data box**. It installs `tracks-bridge` without
starting it, applies migration 0008 through the loader's restart, and writes
`/etc/adhunters/tracks-bridge.env` with two `FILL_ME`.

### 2. The collector patch

`platform/switch-over/collector-scraping-off.patch` is one commit for
adhunters-collector (on top of `e20148c`). Unset, it changes nothing: the
sweeper runs as today. On omarchy:

```
cd ~/Work/adhunters/adhunters-collector
git pull
git am ~/adhunters/platform/switch-over/collector-scraping-off.patch   # the path of your adhunters checkout
make test
make db-migrate                    # 042: spy.walk_queue (a new table, nothing else changes)
make deploy                        # restarts the collector: a few seconds without scraping, as every deploy
git push
```

### 3. Check the proxy lines work from the worker

The old `PROXIES.md` says the datacenter and ISP lines only work from
bigworker's IP; the providers were re-checked on 28 Sep and use passwords only.
The capture test run on 29 Sep settles it. On the data box:

```
sudo -u postgres psql -d adhunters -c "
SELECT l.code, s.outcome, s.status, count(*)
FROM tracks.scrape s LEFT JOIN tracks.proxy_line l ON l.id = s.proxy_line_id
WHERE s.at >= '2026-09-29 02:00Z' AND s.at < '2026-09-29 03:00Z'
GROUP BY 1, 2, 3 ORDER BY 1, 2, 3"
```

Every line mostly `ok` with status 200: go on. A line with only `http` 403:
that provider still has an allowlist; add the worker's IP (2.28.193.220) there
first, or keep that line on bigworker.

### 4. Let the data box reach the collector's database

On prodbox (`ssh prod`), allow the data box's Tailscale address for the
collector's owner login (the bridge creates sighting partitions, which only
the owner can):

```
f=$(sudo -u postgres psql -tAc 'SHOW hba_file')
echo 'host adplatform_v2 adplatform 100.84.83.42/32 scram-sha-256   # tracks-bridge (adhunters-data)' | sudo tee -a "$f"
sudo -u postgres psql -c 'SELECT pg_reload_conf()'
```

From omarchy, copy the login into the bridge's settings without showing it:

```
ssh bigworker "grep '^DATABASE_URL=' /opt/adhunters-collector/secrets/collector.env" \
  | sed 's/^DATABASE_URL=/OLD_DATABASE_URL=/' \
  | ssh admin@adhunters-data "sudo sed -i '/^OLD_DATABASE_URL=/d' /etc/adhunters/tracks-bridge.env && sudo tee -a /etc/adhunters/tracks-bridge.env >/dev/null"
```

If that URL names prodbox by a name the data box can't resolve (`prod`),
change the host to `100.95.67.124` in `/etc/adhunters/tracks-bridge.env`.

### 5. Pick the switch minute and check

The switch minute `C` must be **00:00 UTC** (21:00 São Paulo), so no day is
split between the collector and Tracks. Say it is 1 Oct 2026. On the data box:

```
sudo sed -i 's/^BRIDGE_FROM=.*/BRIDGE_FROM=2026-10-01T00:00:00Z/' /etc/adhunters/tracks-bridge.env
sudo bash -c 'set -a; . /etc/adhunters/tracks-loader.env; . /etc/adhunters/tracks-bridge.env; /opt/adhunters/bin/tracks-bridge check'
```

`check` connects to both databases and says whether `spy.walk_queue` exists,
whether the login can write the collector's tables and owns `spy.sighting`,
and the newest scrape each writer stored. Fix whatever it names, then:

```
sudo systemctl enable --now tracks-bridge
```

It waits for files from `C` on, so starting it early is harmless.

## The night (23:55 to 00:05 UTC)

The proxy lines are split, so the collector and Tracks never share one:

| Box | Lines | For |
|---|---|---|
| bigworker (collector) | `dc-us-2`, `isp-7`, `isp-8`, `isp-9`, `res-1` | the landing page walker and the old Raposa's visits (and the sweeper, for the last 5 minutes) |
| worker (Tracks capture `@a` and `@b`) | `dc-us-1`, `dc-us-3`, `dc-us-4`, `dc-us-5`, `isp-6`, `isp-10` | scraping |

The standby's capture stays off: it would share the worker's lines.

**23:55, bigworker: keep only its lines.** As root on bigworker:

```
cd /opt/adhunters-collector/secrets
cp -p proxies.env proxies.env.before-switch
grep -E '^(dc-us-2|isp-7|isp-8|isp-9|res-1)=' proxies.env.before-switch > proxies.env
systemctl restart adhunters-collector
```

**23:56, worker: its lines, then capture.** From omarchy (the collector's
checkout holds `secrets/proxies.env`):

```
grep -E '^(dc-us-1|dc-us-3|dc-us-4|dc-us-5|isp-6|isp-10)=' secrets/proxies.env \
  | ssh admin@adhunters-worker "sudo sh -c 'umask 027; cat > /etc/adhunters/tracks-capture/proxies.env; chgrp tracks /etc/adhunters/tracks-capture/proxies.env'"
ssh admin@adhunters-worker "sudo test -s /etc/adhunters/tracks-capture/targets.yaml && echo targets ok"
ssh admin@adhunters-worker "sudo systemctl enable --now tracks-capture@a && sleep 5 && sudo systemctl enable --now tracks-capture@b"
```

For these five minutes both scrape, each on its own lines. Tracks' scrapes
before `C` are replaced by the collector's in the history import.

**00:00, bigworker: the sweeper off.**

```
echo 'SWEEPER_CONCURRENCY=0' >> /opt/adhunters-collector/secrets/collector.env
systemctl restart adhunters-collector
journalctl -u adhunters-collector --since -1min | grep 'Scraping is off'
```

Then on omarchy put the same two changes in the checkout's
`secrets/proxies.env` and `secrets/collector.env`, because `make deploy`
copies them over bigworker's.

**00:05, check.** On the data box:

```
sudo bash -c 'set -a; . /etc/adhunters/tracks-loader.env; . /etc/adhunters/tracks-bridge.env; /opt/adhunters/bin/tracks-bridge status'
```

Pending files a few, lag under 2 minutes, nothing quarantined. On prodbox the
newest scrape should come from `tracks:a` or `tracks:b`, and the rate should
stay near 14,000 an hour:

```
sudo -u postgres psql -d adplatform_v2 -c "SELECT worker_node, max(scraped_at), count(*) FROM spy.scrape WHERE scraped_at > now() - interval '15 minutes' GROUP BY 1"
curl -s localhost:8085/status     # on bigworker: queue_new near 0 means the walker gets its links
```

Then open app.adhunters.pro and check the newest ads have minutes-old
sightings.

## The next day: the history import

The new Spy reads Tracks, so it needs the collector's history there. On the
data box, after 00:10 UTC so the last hour before `C` is closed:

```
sudo bash -c 'set -a; . /etc/adhunters/tracks-loader.env; . /etc/adhunters/tracks-bridge.env; /opt/adhunters/bin/tracks-loader import-old -before 2026-10-01T00:00:00Z'
```

It reads prodbox and never writes it. It copies the lookups (creatives, ads,
accounts, campaigns, brands, publishers, placements, links, network ads), then
day by day the hourly and daily counts, each day in one transaction. It copies
counts, not sightings: Tracks keeps sightings only 3 days anyway. Those hours
become **imported hours**: the loader never closes them again and a replay
refuses them. Running it again replaces what the last run wrote. It prints what
it copied, including any publisher whose name differs between the two.

It reads a lot from a busy prodbox: run it after 03:00 São Paulo, and if
prodbox struggles (scrape writes slow in `tracks-bridge status`), stop it with
Ctrl+C and rerun with `-from` a later day; every day already copied stays.

## Rollback

Back to the collector scraping, at any time, nothing lost:

1. Worker: `sudo systemctl disable --now tracks-capture@a tracks-capture@b`
2. bigworker, right after:
   ```
   cd /opt/adhunters-collector/secrets
   cp -p proxies.env.before-switch proxies.env
   sed -i '/^SWEEPER_CONCURRENCY=/d' collector.env
   systemctl restart adhunters-collector
   ```
   (and the same two files on omarchy).
3. Data box: wait until `tracks-bridge status` shows nothing pending (the last
   minute of capture's files), then `sudo systemctl disable --now tracks-bridge`.

The rows the bridge wrote stay: they are real scrapes, marked
`worker_node LIKE 'tracks:%'`. The collector patch can stay deployed; unset,
it does nothing. `spy.walk_queue` empties itself within an hour.

## Later

- The new Spy launches on Tracks; then the bridge, the collector and prodbox
  stop, in that order, each on the owner's word.
- Before prodbox is ever switched off, its raw history goes to the archive:
  `public.adhunters_sighting` (26 GB), `spy.sighting`, and the gz files on
  bigworker. That is its own piece of work.
- The standby's capture needs lines of its own before it can take over.

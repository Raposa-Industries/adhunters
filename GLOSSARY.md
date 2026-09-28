# Glossary

One meaning per word, everywhere: code, UI, docs and alerts.

| Word | Means |
|---|---|
| scrape | One request to an ad network's feed for one publisher and device. Errors count as scrapes. |
| sighting | One ad seen in one scrape. |
| creative | The image and headline pair an operator runs. Many ads can share one. |
| operator | Who runs ads: accounts, sites and brands grouped as one business. |
| stopped | A creative not seen for 6 hours while its usual publishers were scraped. (Not "deactivated".) |
| closed hour | An hour whose raw files are all loaded, 5 minutes after it ended. Counts are computed once per closed hour. |
| open hour | The current hour, rewritten every 5 minutes. |
| raw file | One minute of one capture instance's responses, as received, compressed, in the archive. |
| replay | Re-parsing raw files over a time range, into live or shadow tables. |
| archive | Object storage holding every raw file for good. The shipper writes to it; the loader and replays read from it. |
| dirty hour | An hour whose facts changed since its counts were last computed (a file loaded or loaded again). It closes again. |
| quarantined | A raw file set aside after 3 failed loads, or at once when loading again cannot help. It waits for a fix and a replay. |
| keep time | How long a table keeps rows in the database before its oldest days are dropped. The archive keeps everything, so a replay can bring them back. |
| live link | A click link seen in the last 15 minutes, with the values the ad network put in it. Raposa takes each one once. |
| books balance | The loaded facts add up: each raw file's scrapes equal its rows, and each closed hour's counts equal its sightings. |
| watch | Something a person follows in an app (an operator, an investigation). Watches notify through Pushcut. |
| alert | A system or developer message. Alerts go to Telegram, never Pushcut. |
| spool | Capture's local folder of raw files not yet archived. Capture writes there and nowhere else. |
| sealed | A raw file whose minute has ended, compressed with zstd, ready to archive. |
| shadow run | A new binary running beside the one in service on the same input, writing only its own output, to measure or compare. Nothing reads its output as real data. |
| Direction | Which way an ad is moving: rising, steady, fading or stopped, judged against its usual value. |

## Spy

Every Spy number is defined in [spy/METRICS.md](spy/METRICS.md).

| Word | Means |
|---|---|
| check | One scrape, seen from Spy: one look at one publisher on one device. Rates are per check. |
| presence | Sightings per 100 checks of the network. How often you would see the ad if you looked now. |
| share of voice | A subject's share of all the network's sightings (or its vertical's) in a range. |
| range | Any start and end a number is asked for. Read to the hour when recent, in whole UTC days when older. |
| usual | What a range is compared with: the same hours 1 to 3 weeks before, the weeks just before, or a period the person chooses. Direction's usual value is the same idea for right now. |
| momentum | How presence moved against usual, as a ratio: 1 no change, 2 twice as present. Each publisher and device is compared only with itself. |
| likely range | Where the true value lies 90% of the time. Every change carries one. |
| noise | How much more a subject's counts vary than pure chance (budgets are paced, so ads come in clumps). Ranges and Direction widen by it. |
| clear | A rise or fall big enough and sure enough to name: at most 1 in 10 of a list's clear words is wrong. |
| likely | A change whose likely range leaves out "no change", but not clear enough to name rising or fading. |
| unclear | "Can't tell yet": neither a clear change nor clearly steady. |
| too little data | Not enough sightings to say anything; no number is shown. |
| new | First seen in the last 3 days, or with no usual period to compare with. |
| running | Seen in the last 48 hours (as of a range's end). A creative not running has ended. |
| lifespan | First sighting to last, for a creative; compared with the others of its vertical as the share that ended younger. |
| launch | An operator's creative first seen in a range. |
| hit rate | The share of an operator's launches with an outcome that ran 7 days or more (a hit) rather than ending sooner (a miss). |
| Size | A subject's share and rank in its vertical over 24 hours and 7 days. Scaled: among the few creatives that make half their vertical. |
| stage | Retired: the collector's blend of counts into test, scaling and winner. Use momentum, lifespan and new. |

## Raposa

| Word | Means |
|---|---|
| investigation | Raposa's look at one creative (and, when given, one of its ads): visits under climbing disguises until one sees the dark page, then a sample of visits on that rung. |
| deep investigation | The whole ladder, then the sample, retried while it finds no dark page. Its pages are kept whole. |
| quick investigation | Only the rungs that cost nothing, the landing page's HTML only. No sample, no retry, nothing kept whole. |
| white page | What an operator shows an ad network reviewer: the page the saved link opens from a reviewer's line. |
| dark page | A page a visit reached that is not the white page: another page on the same site, or another site. |
| dark funnel | The dark page and the pages its calls to action lead to, up to checkout. |
| variant | One distinct dark funnel (the same pages in the same order). An operator can rotate several; the sample measures each one's share. |
| disguise | Who a visit pretends to be: the line, the device, the link, the engine (plain fetch or real browser) and what it loads. |
| ladder | The disguises in order of cost. A rung is one step on it. |
| rung | One disguise's place on the ladder, 1 (the reviewer baseline) upward. |
| breach rung | The first rung whose visits saw the dark page. The sample runs there. |
| visit | One load of the ad's link under one disguise, and the funnel steps it followed. |
| sample | The visits on the breach rung after the ladder, spread over a window, that measure the variants and the confidence. |
| line | One proxy exit (datacenter, ISP or residential), named by its key in proxies.env. |
| saved link | The ad's click link as Tracks stored it. Cloakers show the white page to it; only the reviewer baseline uses it. |
| click id | The value an ad network adds to each click link (Taboola's tblci). A live link carries a fresh one. |
| tracker | The redirect service between the ad and the landing page (RedTrack, Voluum and the like). |
| avoided place | A city or state an ad network has an office in. A visit that must see the dark page never exits there. |
| burned line | A line a site shows the white page to while other lines on the same rung see its dark page. Investigations of that site skip it. |
| burned in general | A line burned for many sites (the burn_general_sites setting). Every investigation skips it. |
| version | One distinct text of one page (its host and path). A page that changes gets a new version; one that only changes its noise does not. |
| capture | What a visit stores of a page: its HTML, text and what is read from them. |
| keep | Storing one version whole, once, after the visits: the HTML after its scripts ran, its files and its video. The result is the kept copy. |
| kept copy | A version kept whole, with its files in object storage. |
| evidence | A landing page an investigation reached, with what it proves about who runs the ad: account, checkout, seller, pixels. Published in raposa_api.evidence_v1. |

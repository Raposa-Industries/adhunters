# Spy's numbers

What each number in Spy means, how it is computed, how precise it is for a
given range, and how we checked it does not mislead. The method comes from
the Spy metrics handbook (the research thread, September 2026); the SQL is
in [`migrations/sql/0003_ranges.sql`](migrations/sql/0003_ranges.sql) and the
checks in [`numbers/checks_test.go`](numbers/checks_test.go).

The rule under all of them: **every number is sightings per check, built
from hourly counts.** Tracks gives us, per hour, ad, publisher and device,
how many times the ad was seen (sightings) and how many times we looked at
that publisher on that device (checks, which Tracks calls scrapes). A raw
count of sightings mostly measures how much we scraped; sightings per check
measures the ad.

## Any range

Every list can be asked for any start and end (`spy_api.creative_range_v1`,
`operator_range_v1`, `vertical_range_v1`, `publisher_range_v1`), and
`spy_api.range_info_v1` says what was actually read. "Who had the most
momentum last weekend" is `creative_range_v1('2026-10-17 03:00Z',
'2026-10-19 03:00Z')` ordered by `momentum_rank`.

Every range is compared with a **usual** period:

| Range length | Usual period | Compared |
|---|---|---|
| under 7 days | the same hours 1, 2 and 3 weeks before (only the weeks the subject already existed) | hour of the week with hour of the week, publisher with publisher, device with device |
| 7 days or more | the whole weeks just before it, as many as the range is long (rounded) | day of the week with day of the week, publisher with publisher, device with device |
| any, if the caller chooses one | the chosen period | publisher with publisher, device with device |

### Precision

| Range | Ends are read to | Read from |
|---|---|---|
| under 3 days, usual period inside the last 35 days | the nearest hour | hourly counts, every hour |
| 3 days or more, usual period inside the last 35 days | the nearest hour | daily counts for the whole UTC days, hourly counts for the hours at each end |
| usual period starting more than 35 days back | the nearest UTC midnight | daily counts only |

A range never reaches past the last hour Tracks has closed (5 minutes after
the hour ends); `range_info_v1.to_at` says where it stopped. The 35 days
(`hourly_days`) and 3 days (`hour_cells_days`) are settings: Tracks keeps
every hourly count for now, so `hourly_days` can grow until those move to
Parquet.

Hours with no check at all (collection was down) are counted in
`range_info_v1.hours_unchecked`. They do not bias a rate, because a missing
hour has neither sightings nor checks, but they make ranges wider; say so
beside the numbers when it is above zero. Ranges of 3 days or more compare
whole days, so an outage within a day there is only balanced across
publishers, not across hours: a range with a long outage at a busy hour
reads a little low.

## The numbers

### Presence
`presence`: sightings per 100 checks of the network, in the range.
`presence_usual` the same in the usual period. `phone_presence` and
`desktop_presence` split it by device. It answers "how often would I see
this ad if I looked at the network now".

### Share of voice
`share_pct`: the subject's share of all the network's sightings in the
range. `share_usual_pct` the same in the usual period, and
`share_gain_pts` the difference in points. For creatives,
`vertical_share_pct` is the share within its vertical, and `scaled` marks
the few creatives that make half of their vertical (Size).

### Momentum
`momentum`: how the subject's presence moved against its usual, as a ratio
(1 = no change, 2 = twice as present). It is the **Mantel–Haenszel rate
ratio**:

    momentum = Σ a·T0/T  ÷  Σ b·T1/T

summed over strata (publisher × device × slot of the week), where `a` and
`b` are the subject's sightings now and usual, `T1` and `T0` the checks now
and usual, and `T = T0 + T1`. Each publisher is compared only with itself
at the same hour of the week, so checking one publisher more, a publisher
leaving the feed, or the daily cycle cannot fake a change.

`momentum_low` and `momentum_high` are its **likely range**: the true
ratio lies inside it 90 % of the time. The variance is Greenland and
Robins', multiplied by the subject's **noise** (`noise`, φ): how much more
its counts vary than pure chance. φ is measured on the range itself (the
Pearson dispersion of the conditional binomial fit over the strata) when
there are at least 200 sightings over more than 4 strata; otherwise it is
the subject's daily measure (`spy.dispersion`), else the list's median.
When one side is zero, the range is a one-sided Poisson bound.

`momentum_word` and `momentum_sure`:

| Word | When | Sure |
|---|---|---|
| `rising` | the change passes Benjamini–Hochberg at 10 % across the list, and momentum ≥ 1.25 | `clear` |
| `fading` | the same, and momentum ≤ 0.8 | `clear` |
| `steady` | the likely range sits inside 0.8 to 1.25 | |
| `unclear` ("can't tell yet") | anything else | `likely` when the range leaves out 1, else empty |
| `new` | no usual period: the subject did not exist yet | |
| `too_little` ("too little data") | under 10 sightings in both periods together (counted in clumps of its noise), a likely range wider than 3×, or a zero side whose bound does not clear 0.8 or 1.25 | |

No momentum number is shown for `too_little` and `new`: the columns are
NULL.

`momentum_rank` answers "who had more momentum": it orders by the
**shrunk** log ratio, `ln M · τ² / (τ² + Var)` with `τ²` the spread of real
changes on the network. A small ad that went from 1 sighting to 3 has a
huge ratio and a huge variance, and shrinks toward no change; a big ad
that doubled keeps almost all of it.

### Lifespan (creatives)
`lifespan_days`: first sighting to last, or to the range's end if it is
still `running` (seen in the 48 hours before the end). `lifespan_pct`:
the share of creatives of the same vertical, first seen in the 90 days
before, that ended younger (Kaplan–Meier, so the ones still running count
right). `lifespan_curve` names the curve: the vertical, or `*` (all
creatives) when the vertical has fewer than 20.

### Launch hit rate (operators)
`launches`: the operator's creatives first seen in the range. A **hit**
ran 7 days or more; a **miss** ended sooner (not seen for 48 hours);
`testing` has no outcome yet. `hit_rate_pct` is hits ÷ (hits + misses),
with a Wilson 90 % range (`hit_rate_low_pct`, `hit_rate_high_pct`), shown
once 5 launches have an outcome.

### Direction (the live one)
Direction is momentum for right now: the last 2 hours against the same
hours of the usual weeks, rerun every 5 minutes. Its gap is
`z = (seen − expected) / √(noise · max(expected, 1))`, and entering rising
or fading also needs Benjamini–Hochberg across the run. It keeps its old
words (rising, steady, fading, stopped) and hysteresis; see
[README](README.md#direction).

### Retired
Stage, the conviction index, volatility and half-life are gone. Each was a
blend of numbers with weights nobody could check, and the handbook's
simulation showed they flag noise as signal. `is_new` (first seen in the
last 3 days) and `running` replace Stage's useful parts.

## Adding a number

Every list is built from `spy.subject_range(kind, …)`, which returns
`spy.range_row`. A number that every kind needs goes there once; a kind's
own numbers go in its function (`spy.creative_range`, `spy.operator_range`).
A new kind of subject (a brand, a landing page host) is a new `kind` with
its key: `subject_range` joins the cells to the key and does the rest. New
inputs (spend estimates, clicks, landing page behaviour) fit the same way
as long as they are counts per hour and publisher.

## Checks

The handbook asks six questions of every metric. Four run in
`numbers/checks_test.go` on simulated counts shaped like Tracks' (clumped
sightings at 3× chance, lognormal ad sizes, 28 publishers on 2 devices, a
daily cycle, the São Paulo weekend):

| Check | Asks | Result |
|---|---|---|
| A/A | With nothing changing, how many ads get a word? Do ranges hold the truth 90 % of the time? | 0.6 % labelled (at most 1 % allowed); ranges held the truth 88.3 % of the time |
| Effort | With one publisher checked 10× more, another almost dropped and a 6-hour outage, does momentum move? | at most 1 % labelled; median momentum 0.97 (1 is perfect) |
| Power | With 10 % of ads truly doubling, how many does it find, and is "rising, clear" right? | 33 of 34 "rising, clear" were real; the top 20 were all real |
| Additivity | Do a week's sightings equal its days' and hours'? | exact |
| Stopped accuracy | Does "stopped" mean the ad stopped? | needs real history |
| Predictive value | Does a rising ad keep running longer than a steady one? | needs real history |

The last two need weeks of Tracks' data. Once it has them, a nightly check
should compare last month's words with what happened since, and alert when
"rising, clear" stops predicting longer lives.

With 10 % of ads truly rising, 1 to 2.6 % of the unchanged ads get a word
too (the false discovery rate at work, across seeds): a word is a strong
hint, not proof, and the likely range says how strong.

Timings at Tracks' volume are in [`measure/`](measure/).

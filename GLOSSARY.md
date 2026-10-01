# Glossary

One meaning per word, everywhere: code, UI, docs and alerts.

| Word | Means |
|---|---|
| scrape | One request to an ad network's feed for one publisher and device. Errors count as scrapes. |
| sighting | One ad seen in one scrape. |
| creative | One image or video, found by its file name (`creative_key`). Many ads can share one. (Not the image and headline pair: that is an ad. See [decision 0010](decisions/0010-ad-network-words.md).) |
| ad | One creative with one headline, wherever it runs. The same ad in several campaigns is several items. |
| item | One ad inside one campaign, under the ad network's own id: Taboola's campaign item (`{campaign_item_id}`), NewsBreak's ad (`adId`). |
| campaign | Where an account sets the budget, bid and targeting for its items. On Taboola a campaign; on NewsBreak an ad set. |
| parent campaign | The level above a campaign: NewsBreak's campaign above the ad set. Taboola's campaign group plays that part but never shows in the feed. |
| account | One advertiser account on an ad network, the one that pays: Taboola's alphabetic account id (`acme-sc`), NewsBreak's `adAccountId`. Taboola's feed names this field `publisher`; it is never a publisher. |
| brand | The name shown on an ad ("Health Digest"). On Taboola it is a campaign setting (`branding_text`, 25 characters at most), so a campaign's items all show the same one. On NewsBreak it is the ad's `advertiser`. |
| operator | Who runs ads: the accounts, brands and landing sites of one business, grouped as one. Ad networks never show this grouping, so Spy infers it. Taboola's network accounts, which hold many accounts, are invisible to us. |
| publisher | A site that shows an ad network's ads. Taboola calls it a site (`{site}`, `{site_id}`). In Tracks, one scraped page of it, named as in the targets file. |
| placement | One ad widget on a publisher's page ("Below Article", "Right Rail"), named by the feed answer. |
| tracking code | The query string an account adds to every link in a campaign. The ad network fills its macros (`{campaign_id}`, `{campaign_item_id}`, `{site_id}`, `{cpc}` and so on) on each click, which is how links carry these ids. |
| bid | What an account offers in an auction: `bval` on a Taboola card, `{cpc}` in a link, the bid `price` in a NewsBreak answer. |
| clearing price | What the winning ad paid in one Taboola auction (`auctionPrice` on the card). Its unit is not confirmed yet. |
| auction | One ad slot sold on a publisher: Taboola puts its id, clearing price, the winning bid and cap on each card it shows. Tracks keeps every one a check saw. |
| second price | What the next bid was in a NewsBreak auction (`second_price` in its answer). |
| typical price | The middle of the daily medians over a range, weighted by each day's auctions. Beside the average, it is what a usual auction cost. |
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
| page | An alert that needs someone now: Telegram with sound, repeated every 5 minutes until it clears or is silenced. |
| chat alert | An alert that can wait for the day: Telegram, silent, grouped, 08:00 to 22:00 São Paulo. |
| digest | The one Telegram message at 08:00 São Paulo with yesterday in numbers. |
| heartbeat | The alert that always fires. Better Stack calls when it stops arriving, because then no other alert can be trusted. |
| runbook | The file in `runbooks/` an alert links: what it means, what to check, how to fix it. |
| credit | What is left on a prepaid service we pay for: proxy traffic, AI credit. observe-bot reads it where the service has an API, or estimates it from our own spending where it has none (`credits.conf`). |
| policy page | An article or section of Taboola's advertiser help center (realize.com/help) under Policy & Content Review. observe-bot reads them every 6 hours. |
| policy change | A policy page that appeared, went away or whose text changed between two crawls. Each one is posted to "AdHunters alerts". |
| renewal | The day a subscription we depend on renews or must be paid, for services with no balance to read (the datacenter and ISP lines). |
| task | Work a service does on a schedule (an hour close, a refresh, a pull). Each promises how often it succeeds; one past its promise is late. |
| spool | A collector's local folder of what it received and has not handed on yet: Capture's raw files not yet archived, intel-collect's answers not yet in the database. The collector writes there first, so it keeps working while the database or archive is away. |
| sealed | A raw file whose minute has ended, compressed with zstd, ready to archive. |
| shared code | Code several services need, kept in one shared module (`kit`, `contract` or `shared/`) that each imports, never copied between services or imported from another service's folder. See [decision 0013](decisions/0013-shared-code.md). |
| shadow run | A new binary running beside the one in service on the same input, writing only its own output, to measure or compare. Nothing reads its output as real data. |
| switch-over | The minute Tracks alone scrapes and the old collector's sweeper stops, always 00:00 UTC. See `platform/SWITCH-OVER.md`. |
| bridge | `tracks-bridge`: writes Tracks' scrapes into the old collector's database after the switch-over, so today's Spy keeps working until the new Spy launches. |
| landing page | The page an ad's link ends on, after the tracker's redirects. |
| walk | `tracks-walker` following one ad's saved link, from its publisher's page, through the redirects to the landing page and one next step. Saved whole as a raw walk file first. |
| next step | The page a landing page's main button (its call to action) leads to, often an order page. A walk takes at most one. |
| page version | One distinct content of a page (title, headings, text, pixels, contacts), kept once however often it is seen. |
| seller | The merchant account on a checkout platform (a ClickBank vendor, a Digistore24 product owner) that a page names. |
| imported hour | An hour before the switch-over whose counts were copied from the old collector's database (`import-old`). The loader never closes it again and a replay refuses it. |
| Frame | The shell every app's pages sit in (`shared/frame`): the app selector, page tabs, ⌘K search and account in the top bar, and a left column that holds only filters. |
| mascot | The AdHunters fox: the original artwork, cut out of its background, in `shared/frame/assets/mascot.svg`. Every app shows it in its crouch, breathing and swaying its tail, while a page loads. (Not Raposa: that is the app that investigates landing pages.) |
| Direction | Which way an ad is moving: rising, steady, fading or stopped, judged against its usual value. |

## Create

| Word | Means |
|---|---|
| session | One chat in Create: a vertical and a name, which are also the library folders what it saves goes in (`<vertical>/<session>`). The same name in the same vertical is the same session. |
| turn | One message a person sends in a session: a prompt, the items they picked, and how many pictures and headlines to make from them. |
| item | One picture or headline in a session: made by a turn, or brought in from the computer, the library or typed. Any item can be picked for the next turn. |
| pick | Choosing items in a session, for the next turn to start from or to save. Picked pictures are changed as the prompt says (or varied, with no prompt); picked headlines are varied. |
| brief | The words Create gives the picture model for one picture. Before 1 Oct 2026 (decision 0015) also the starting point of the old brief pages, replaced by sessions (decision 0019). |
| performing ads | Ads a person gives Create because they are doing well. Create reads them for their pattern (what is fixed, what can vary) and makes new images in it; they are never sent to the image model. |
| angle | The kind of image an idea is: a close variation of what works, or a new way of showing the product ("Colher", "Canudo", "Reação depois de tomar"). A text call names one for each brief it writes. |
| blocked words | The team's list of words and phrases Taboola has blocked for them in titles (a few in descriptions too). Create avoids them, warns about them and offers other words in their place; it never refuses them. |
| analysis | What the performing ads share, aspect by aspect: what to keep and what can vary. Create writes it when it reads them; the person edits it before making options. |
| save | Picked items written into the library, in their session's set (folder), with the person's AI label for made pictures. |
| pairing | How the chosen creatives, headlines and CTAs become ads. Mixed: the longest list once each, the shorter ones repeated in turn until it runs out (10 images, 5 headlines and 2 CTAs make 10 ads). One to one: the person picks each pair. Every combination: each creative with each headline and each CTA. |
| CTA | The button on an ad ("Read More", "Learn More"). Taboola takes one per item, from its own list; a person may pick several and pairing spreads them. |
| ad id | Our own id for an ad: `ah-`, then the first 10 hex characters of the creative's SHA-256, a dash, and the first 10 of the headline's. It goes in Taboola's Custom ID (30 characters at most), so results can come back to the ad. |
| bulk sheet | Realize's own template for Create › Bulk Upload, with our ads written into its Ads tab (one row per ad, its campaign ids together in one cell), plus a ZIP of the images named in "Image File Name". |
| campaign group | Taboola's level above a campaign, which can hold a budget shared by its campaigns. Taboola makes an "AutoGen" one for a campaign made without a group, and a campaign's group is set only when it is made. |
| paused | Made but not running: a group, campaign or item with Taboola's `is_active` false. Everything Create makes on Taboola is paused; only a person turns it on, in Taboola's own dashboard. (Not "stopped", which is Spy's word for a creative no longer seen.) |
| lent account | A Taboola login someone else owns that we may use for tests (ZoltaGroup). create-web runs it only-own: it lists and touches only the groups and campaigns it made, as recorded in its state file. |

## Library

| Word | Means |
|---|---|
| library | The creatives and headlines the team keeps, shared by Create and Launch, with a copy of each file in the team's Google Drive folder. The apps never call it Drive. See [decision 0014](decisions/0014-library.md). |
| set | Creatives and headlines made or uploaded together: one session in Create, one folder in Drive. Launch picks a set to make ads from. |
| safe copy | Retired (decision 0020): the library's own copy of a picture's bytes. The library keeps files in Drive only. |
| minted name | A creative's name from its vertical's code, the network letter and a counter that only goes up (`BPT43`), given when an app saves it. Never given twice. |
| hidden | Taken out of the library's lists by a person. Nothing in the library is deleted. |

## Launch

| Word | Means |
|---|---|
| group | Short for campaign group, in Launch's tree (Network > Account > Group > Campaign > Ads). On NewsBreak, its campaign. |
| pair | One desktop and one mobile campaign Launch made together (a new campaign for both devices), with the same settings and the same ads, in one group. Named with the team's names (CMP<n>-<account>-Desktop-pp-bl and CMP<n>-<account>-Mobile-pp-bl) or "<name> · Desktop" and "<name> · Mobile". Mobile means phones only. |
| preset | Settings a person saved to reuse, at one of two levels: a group preset (budget and objective) or a campaign preset (bid, caps, countries, link and tracking code, brand, description). For one account or all. Nobody fixes them for the team. |
| copy | A new, paused campaign with another's settings and ads (Taboola's duplicate with its items; the ads get new ids and go through review again). It has its own id. |
| move | Putting a campaign in another group. Taboola fixes a campaign's group when it is made, so a move is a copy in the new group; the original is paused when a person starts the copy (or at once, or never, as the person chose). |
| draft | A new pair saved on the server to finish later. It is deleted when the pair is made. |
| History | Everything Launch did on a network, one row per change: who did it, who asked (a person, Intel or Desk), before and after, and the result. |
| old launcher | create-web's one-page campaign launcher, the prototype Launch replaced. Retired on 2026-10-01: create-web now only redirects to Launch. |
| request | A change another service (Desk) asks Launch for. It waits in Launch's Pedidos until a person confirms it, and only then is sent; or the person refuses it. |

## Intel

| Word | Means |
|---|---|
| traffic source | The tracker's record for one ad network (RedTrack's `source`): the macros its tracking code asks the network to fill, and which sub slot each lands in. |
| sub slot | One of the tracker's numbered click parameters (`sub1` to `sub20` in RedTrack). The traffic source decides what fills each: a campaign id, an item id, a site id. Reports can be grouped by them. |
| tracker campaign | The tracker's campaign: one link, its traffic source and where it sends clicks. Usually one per ad network campaign, but nothing enforces it; the join goes through the sub slots, not the name. |
| offer | What a tracker campaign sells: the product page or affiliate link the clicks end on, and what a sale pays. |
| conversion | One sale (or other event) the offer reported back to the tracker for one click, with its payout. |
| cost | What the ad network charged for clicks, as the tracker knows it: filled from the `{cpc}` in each link or pulled from the network's API. |
| revenue | What conversions paid, as the tracker counts it. Profit is revenue minus cost. |
| answer | One reply from Taboola or RedTrack to one request, kept as received (intel.answer) before anything reads it. Intel's tables are parsed from answers and can be parsed again over any range. |
| window | The stretch of whole days a result covers, in the account's time zone: today (so far), yesterday, 7 days or 30 days (both ending today). |
| result | A campaign's or ad's numbers over one window: Taboola's impressions, clicks and spend; the tracker's clicks, landing page views and clicks, sales and revenue; and rates with their likely range. |
| step rates | The rates from impression to sale, one step at a time: CTR (clicks per impression), landing page click rate (landing page clicks per tracker click) and sale rate (sales per landing page click). |
| profit per 1,000 | Profit per 1,000 impressions: how Intel compares an ad with its campaign, since it counts both what a click costs and what it earns. Measured from an ad's own sales once it has 10; estimated from its step rates, with the sale rate borrowed from its campaign, before that. |
| verdict | How an ad compares with its campaign on profit per 1,000, in Spy's words: better, worse, usual (within 20% of the campaign), unclear or too little data; better and worse are clear or likely. |
| spend to tell | For an unclear ad, about how much more spend would make its verdict clear, if it kept its present rates. |
| usual cost per sale | An account's spend over its tracker sales in the last 30 days. Runaway alerts and pause suggestions ask how often an ad that sells at this cost would go this far without a sale. |
| runaway | A campaign spending today with no sale, past what an ad at the usual cost per sale would reach without one 1 time in 50 (and past $20). Spend from the last hour is left out: its sales may not be in yet. |
| tracking gap | The tracker counted under half the clicks the network counted in the last full hour: links lose their tracking or the tracker is down. |
| landing page gap | The tracker's landing page views were under 30% of its clicks in the last full hour: the page does not open, or its script does not fire. |
| postback gap | The network counted under half the tracker's sales yesterday: sales are not reaching the network, so its own optimisation works blind. |
| delivery status | A campaign's state on the ad network, as Taboola's "Delivery Status" shows it: running, paused, pending approval, rejected, depleted (its budget is spent), expired, terminated, frozen; deleted when it leaves the network's list. Intel sends each change to "AdHunters alerts". |
| suggestion | A change Intel proposes (pause these ads, pause this campaign, lower the daily cap), with why. Intel never makes it: its button opens Launch with the change filled in, and a person makes it there. |
| not now | A person setting a suggestion aside. It does not come back for a day, and comes back after that only if its reason still holds. |
| campaign line | A campaign and every copy made of it. Moving a campaign to another group makes a copy with a new id (Taboola cannot change a campaign's group), so Intel keeps the line to follow its history across ids. |

## Spy

Every Spy number is defined in [spy/METRICS.md](spy/METRICS.md).

| Word | Means |
|---|---|
| check | One scrape, seen from Spy: one look at one publisher on one device. Rates are per check. |
| presence | Sightings per 100 checks of the network. How often you would see the ad if you looked now. |
| sparkline | A small line of a creative's presence per day, beside it in a list. |
| vertical | The market a creative sells into ("Blood Pressure", "Joint Pain"), one of the fixed list in [shared/verticals](shared/verticals/verticals.yaml). Never a name a classifier made up. |
| category | A group of verticals ("Heart", "Pain"), for filtering: a category first, then its verticals. |
| catch-all | A vertical that takes what fits nowhere else in its category ("Other health"). The classifier picks it only when no specific vertical scored. |
| unsure | A vertical answer with confidence under 0.6. Lists show it with a question mark; the model may replace it. |
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
| hit rate | The share of an operator's launches with an outcome that ran 15 days or more, past a Taboola A/B test's 14 (a hit), rather than ending sooner (a miss). |
| Size | A subject's share and rank in its vertical over 24 hours and 7 days. Scaled: among the few creatives that make half their vertical. |
| site | Where an operator's landing pages are: a registrable domain, or the whole host on a hosting domain (`shop.myshopify.com`). A checkout platform is never a site. |
| clue | Something a site's pages carry that names who runs them: a pixel id, an email, a company name. A strong clue is one business's; the others are hints and group nothing. |
| grouping | Spy's own answer to which sites and accounts make one operator (`spy.regroup_operators`). Proposed until the `operators_from` setting says `grouping`. |
| hand fix | A person's correction to the grouping: join a site or account to an operator, or keep it out of every group. It always wins. |
| agency | An account name root whose accounts buy for 3 or more operators' sites. Its accounts are not grouped by the root. |
| kind | An operator's business: arbitrage (an account buys for 3 or more site groups), affiliate (sells through an affiliate network) or direct. |
| stage | Retired: the collector's blend of counts into test, scaling and winner. Use momentum, lifespan and new. |

## Desk

| Word | Means |
|---|---|
| conversation | One person's talk with Desk, on Desk's page. Only that person sees it, and Desk acts in it for them, with their rights. |
| turn | Desk answering what is new in a conversation: calls to Claude and the tools it asks for, until it answers in words. |
| action | One thing an app lets a teammate do: one view or function in its `<app>_api`, listed in the action catalog. A read looks; a change changes the app's own data or starts its work; an ask asks the app for something a person then confirms on the app's own screen. |
| action catalog | Every app's actions, one file per app in `contract/actions/`. Desk can do only what it lists, and it lists no confirm and nothing that turns a campaign or ad on. |
| plan | Work Desk proposes in a conversation: a goal and its plan steps, shown with every input. Nothing in it runs before the conversation's person OKs it. A new plan replaces one still waiting for its OK. |
| plan step | One numbered part of a plan ("step" inside Desk): an action (a change or an ask), a choice, or a person's to-do. It can take a value from an earlier one: the id a change returned, the ids chosen. |
| OK | A person's yes to a plan on Desk's page, for the plan exactly as they saw it: a plan changed since is not approved. Confirming an ask is not an OK; that happens on the owning app's screen. |
| choice | A plan step that shows a read's rows (creatives, headlines) with the ones Desk would pick and why. The plan goes on with the rows the person picks. |
| to-do | One piece of work someone holds, a person or Desk: a title, a due date, a link and notes, on one list for everyone. (Not "task", which is a service's scheduled work.) |
| origin | What asked an app for something, passed to its function: `auto` for Raposa's own investigations, `desk:step:<id>` for a plan step (the same on every try, so an app that tracks origins never does it twice). |
| outside column | A read's column holding text someone outside the team wrote (a competitor's headline, a landing page's title). The model gets it cut short and marked as data, never as instructions. |
| stop switch | Stops Desk at once: no call to Claude and no plan step runs until someone starts it again. Anyone on the team can use it. |

## Raposa

| Word | Means |
|---|---|
| investigation | Raposa's look at one creative (and, when given, one of its ads): visits under climbing disguises until one sees the dark page, then a sample of visits on that rung. |
| deep investigation | The whole ladder, then the sample, retried while it finds no dark page. Its pages are kept whole. |
| quick investigation | Only the rungs that cost nothing, the landing page's HTML only. No sample, no retry, nothing kept whole. |
| automatic quick investigation | A quick investigation Raposa queues on its own (origin `auto`) for a new ad that runs now and whose landing page no visit has read whole. |
| usable page | A page read whole: 40 words or more, and not a bot check or an error page (`raposa.usable_page`). |
| imported investigation | An investigation copied from the collector's database by `raposa-engine import-old`, with its visits, pages and files. |
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
| report | Numbers an ad network or tracker gives about our own accounts over a range of days, split one way (by day, campaign, site, item and so on). Taboola's Backstage reports are one. Saved raw before they are read. |
| tracker | The redirect service between the ad and the landing page (RedTrack, Voluum and the like). |
| avoided place | A city or state an ad network has an office in. A visit that must see the dark page never exits there. |
| burned line | A line a site shows the white page to while other lines on the same rung see its dark page. Investigations of that site skip it. |
| burned in general | A line burned for many sites (the burn_general_sites setting). Every investigation skips it. |
| version | One distinct text of one page (its host and path). A page that changes gets a new version; one that only changes its noise does not. |
| capture | What a visit stores of a page: its HTML, text and what is read from them. |
| keep | Storing one version whole, once, after the visits: the HTML after its scripts ran, its files and its video. The result is the kept copy. |
| kept copy | A version kept whole, with its files in object storage. |
| evidence | A landing page an investigation reached, with what it proves about who runs the ad: account, checkout, seller, pixels. Published in raposa_api.evidence_v1. |

## Funnels

| Word | Means |
|---|---|
| landing site | A domain whose pages Funnels hosts, served by `funnels-edge` from its published versions. |
| landing page | One page of a landing site or of a page we don't host, named by its `ah-lp` meta tag or its path (`lp` in code). |
| tracker click id | The id the tracker gives each click (RedTrack's `clickid`), passed to the landing page in its URL. Joins a journey to its conversions. Not Taboola's click id. |
| journey | One person's way through our landing pages from one tracker click: every page, step and video second, until they stop. A landing URL with a new tracker click id starts a new one. (Raposa's visit is something else.) |
| beacon | One batch of events the page script sends to the collector. The edge saves each one as received. |
| step | A point in a journey: a page loaded (`view`), a scroll mark, 10 s in view, a part of the page marked with `data-ah-step`, a counted click or form, or the video's play, pitch and end. |
| drop-off | The journeys whose last step was this one: where they stopped. |
| VSL | A video sales letter: the long video a landing page sells with. Ours play in our own player. |
| play | A journey that unmuted the autoplaying video or pressed play. Muted autoplay alone is not a play. |
| watched seconds | The seconds of a video a journey heard, each counted once; seeking ahead leaves a gap. |
| retention curve | For each second of a video, the plays that heard it. |
| pitch | The second a video starts selling. Set per video; the buy button can appear then. |
| arm | One of the choices an A/B test splits journeys between (a video, a pitch time). A journey always gets the same arm. |
| bot suspect | A journey that looks automated: the browser says so, its user agent is missing or a bot's, it had no input and under 1 s in view, or it came from a data-center network. Kept and flagged, left out of the counts. |
| data-center network | A network a cloud or hosting company publishes as its own (AWS, Google Cloud, or a list imported by hand). People rarely browse from one. |
| draft count | The counts of an hour that has not closed yet, made every few minutes so the pages can show it, marked partial. Never published in `funnels_api`. |
| path test | The CI test in `e2e/` that walks one ad from Spy through Create, the library and Launch to Intel, through the real binaries, with fakes for Taboola, OpenAI and Google Drive. It fails when two apps stop agreeing, or when Launch sends Taboola anything that breaks the owner's rules. |

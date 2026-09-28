# 0010 · Ad network words: creative, ad, item, campaign

**Proposed:** 28 Sep 2026, by Claude, after reading Taboola's Backstage API
reference. It holds once Marcos merges it.

A **creative** is one image or video. An **ad** is one creative with one
headline. An **item** is one ad inside one campaign, under the ad network's
own id (Taboola's campaign item, NewsBreak's ad). A **campaign** is the level
that holds budget, bid and targeting: Taboola's campaign, NewsBreak's ad set.
The glossary also gains account, brand, publisher, placement, tracking code,
bid and clearing price, each tied to the ad network field it comes from.

This changes one meaning. The glossary said a creative was "the image and
headline pair"; it is now the image alone, and the pair is an ad.

**Why:** Tracks and Raposa already work this way (`tracks.creative` is one
image, `tracks.ad` is a creative with a headline, Raposa investigates a
creative "and one of its ads"), and Create makes images and headlines
separately. Only the glossary disagreed. Taboola's own words fit the same
shape: its items are per campaign, its `branding_text` is a campaign setting,
and the card field it calls `publisher` is the advertiser's account. Naming
the ad network field behind each word keeps an agent reading a raw answer
from mapping it to the wrong thing.

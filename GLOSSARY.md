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

-- Published: each distinct page content.
CREATE VIEW tracks_api.page_version_v1 AS
SELECT hash, title, word_count, headings, meta, favicon_url, pixels, emails, phones, companies, disclaimers,
       vsl, text, first_seen_at, last_seen_at
FROM tracks.page_version;

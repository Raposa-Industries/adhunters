// The bulk sheet's rows and the ZIP's names. The rows go into the person's
// own Realize template (template.js); an ad's "Image File Name" must match a
// file in the ZIP exactly.

import { clean } from "./checks.js";

// The Ads tab's headers, in the order of Realize's own template (Create ›
// Bulk Upload › Download Template, read 2026-09-29). Row 1 of that tab is
// notes, row 2 these headers, ads from row 3.
export const AD_COLUMNS = [
  "Ad ID", "Campaign ID", "Landing Page URL", "Ad Name", "Custom ID", "Title", "Description", "CTA",
  "Image File Name", "Image URL", "Motion Ad Video/Gif File Name", "Motion Ad Video URL", "Motion Ad Gif URL",
  "Motion Ads Default Image File Name", "Motion Ads Default Image URL", "Status", "AI Content", "Delivery Status",
];
for (let i = 1; i <= 5; i++) {
  AD_COLUMNS.push(`3rd Party Tracking ${i} - Pixel URL`, `3rd Party Tracking ${i} - Tag Type`, `3rd Party Tracking Pixel ${i} - Event Type`);
}

// The template's CTA Types tab, kept to the ones Backstage's API also takes
// (its cta_type list, read 2026-09-29), so a choice works both ways. Empty
// means no button.
export const CTAS = ["", "Learn More", "Read More", "Shop Now", "Buy Now", "Order Now", "Get Offer", "Get Now",
  "Try Now", "Sign Up", "Watch Now", "Apply Now", "Click Here", "Start Now", "Get Info", "Get Deals",
  "Get Coupon", "Get Quote", "Get Sample", "Try Free", "Save Now", "Explore", "Contact Us", "Call Now", "Book Now",
  "Register", "Subscribe", "Join Now", "Download"];

// ctaType is the API's name for a CTA label: "Learn More" is LEARN_MORE, no
// button is "".
export function ctaType(label) {
  return label ? label.trim().toUpperCase().replace(/\s+/g, "_") : "";
}

// The most ads one template holds: its Ads tab runs to row 500.
export const MAX_ADS = 498;

// campaignIds reads the ids typed in one box: digits, split by anything else.
export function campaignIds(text) {
  return [...new Set((text.match(/\d+/g) || []))];
}

// adRows makes one Ads row per ad, in AD_COLUMNS order. An ad goes into
// every campaign at once ("123; 456" in Campaign ID, as the template asks).
// ads: [{creativeFile, adName, customId, title, cta}]; settings: {campaigns,
// url, description, cta (when an ad has none of its own), ai ("Yes" or "No")}. Status is left empty: Realize sets
// new ads Active and reads the column only on updates.
export function adRows(ads, settings) {
  const at = (name) => AD_COLUMNS.indexOf(name);
  return ads.map((ad) => {
    const row = new Array(AD_COLUMNS.length).fill("");
    row[at("Campaign ID")] = settings.campaigns.join("; ");
    row[at("Landing Page URL")] = settings.url;
    row[at("Ad Name")] = ad.adName;
    row[at("Custom ID")] = ad.customId;
    row[at("Title")] = ad.title;
    row[at("Description")] = settings.description || "";
    row[at("CTA")] = (ad.cta ?? settings.cta) || "";
    row[at("Image File Name")] = ad.creativeFile;
    row[at("AI Content")] = settings.ai || "";
    return row;
  });
}

// tsv turns rows into tab-separated text to paste under the header of the
// Ads tab in Taboola's own template.
export function tsv(rows) {
  return rows.map((r) => r.map((v) => String(v).replace(/[\t\r\n]+/g, " ")).join("\t")).join("\n");
}

// safeName turns a file name into one that survives every tool on the way:
// ASCII letters, digits, dot, dash and underscore, lower-case extension.
export function safeName(name) {
  const dot = name.lastIndexOf(".");
  let base = dot > 0 ? name.slice(0, dot) : name;
  let ext = dot > 0 ? name.slice(dot + 1).toLowerCase() : "jpg";
  if (ext === "jpeg") ext = "jpg";
  base = base.normalize("NFKD").replace(/[̀-ͯ]/g, "").replace(/[^A-Za-z0-9._-]+/g, "-").replace(/-+/g, "-").replace(/^[-.]+|[-.]+$/g, "");
  ext = ext.replace(/[^a-z0-9]/g, "") || "jpg";
  return `${(base || "imagem").slice(0, 80)}.${ext}`;
}

// uniqueNames makes safe names unique, ignoring case: a.jpg, a-2.jpg, ...
export function uniqueNames(names) {
  const seen = new Set();
  return names.map((n) => {
    const safe = safeName(n);
    const dot = safe.lastIndexOf(".");
    let candidate = safe;
    for (let i = 2; seen.has(candidate.toLowerCase()); i++) candidate = `${safe.slice(0, dot)}-${i}${safe.slice(dot)}`;
    seen.add(candidate.toLowerCase());
    return candidate;
  });
}

async function sha256hex(bytes) {
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// fingerprint is the first 10 hex characters of a SHA-256.
export async function fingerprint(bytes) {
  return (await sha256hex(bytes)).slice(0, 10);
}

// adId is our id for one ad, sent as the item's Custom ID (30 characters at
// most): "ah-", the creative's fingerprint, "-", the cleaned headline's. The
// same picture with the same headline gets the same id in any bundle and
// campaign, so Taboola's reports can name the ad later. With a CTA, the CTA
// joins the headline in the second hash, so the same picture and headline
// under two buttons are two ads with two ids.
export async function adId(creativeFingerprint, headline, cta = "") {
  const text = cta ? `${clean(headline)}\u0000${cta}` : clean(headline);
  const h = await fingerprint(new TextEncoder().encode(text));
  return `ah-${creativeFingerprint}-${h}`;
}

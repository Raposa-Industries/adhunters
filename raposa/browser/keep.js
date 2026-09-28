'use strict';

// Raposa keeper: POST /keep.
//
// Keeps one version of one page whole, once: the HTML after its scripts ran,
// every stylesheet (and the files the stylesheets name), script, image and
// font it loads, and its video. Ported from adhunters-collector e20148c
// (browser/keep.js), itself from adplatform-v2 (internal/walk/deepfunnel.go
// keepAssets, internal/walk/video.go).
//
// The page is not asked for again. Its HTML is the copy a visit already
// stored, served to the browser in place of the document, so the cloaker
// decides nothing and the version kept is exactly the version the visit saw.
// Every other request (stylesheets, scripts, pictures, fonts, playlists, video
// pieces) goes out on the line the collector chose, through the browser's own
// request context, with the page as referer.
//
// Files are written under the directory the engine names (it runs on the
// same machine), so a file never travels inside the JSON answer. A plain
// video file is not downloaded here at all: the answer names it with
// download: true and the engine streams it to disk through the same line, so
// a 400 MB file is never held in this process's memory.
//
// Words follow GLOSSARY.md: page, version, capture, keep, visit.

const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const NAV_TIMEOUT_MS = 45000;
const SETTLE_MS = 8000;
const DEFAULT_MAX_FILE_BYTES = 26214400; // 25 MB, one file
const DEFAULT_MAX_VIDEO_BYTES = 400 * 1024 * 1024; // one stitched video
const DEFAULT_MAX_SEGMENTS = 3000;
const DEFAULT_MAX_VIDEOS = 3;
const MAX_CSS_FOLLOW = 200; // files named by stylesheets that the page did not load
// One page gets 10 minutes (agreed 2026-09-27: one video page held the only
// keep slot for 40). Past it the video downloads stop and the page is kept
// with what it has, each cut video saying where it was cut. The alarm is the
// last resort for a page that hangs before its videos.
const KEEP_BUDGET_MS = 10 * 60 * 1000;
const KEEP_TIMEOUT_MS = 13 * 60 * 1000;

// Video pieces and media are not let through while the page plays: the
// video is fetched once, whole, afterwards. Playlists are let through, since
// they are how a player says where its video is.
const SEGMENT_RE = /\.(ts|m4s|m4v|m4a|aac|mp3|mp4|webm|mov)(\?|$)/i;
const PLAYLIST_RE = /\.(m3u8|mpd)(\?|$)/i;
const PLAYER_HOSTS = ['youtube.com', 'youtube-nocookie.com', 'youtu.be', 'vimeo.com'];

function roleOf(resourceType, url, mediaType) {
  switch (resourceType) {
    case 'image':
      return 'image';
    case 'stylesheet':
      return 'stylesheet';
    case 'script':
      return 'script';
    case 'font':
      return 'font';
    case 'media':
      return 'video';
  }
  const mt = (mediaType || '').toLowerCase();
  if (PLAYLIST_RE.test(url) || mt.includes('mpegurl')) return 'playlist';
  if (mt.startsWith('image/')) return 'image';
  if (mt.startsWith('font/') || mt.includes('font')) return 'font';
  if (mt.includes('css')) return 'stylesheet';
  if (mt.includes('javascript')) return 'script';
  return null;
}

function withoutFragment(u) {
  try {
    const x = new URL(u);
    x.hash = '';
    return x.href;
  } catch (e) {
    return '';
  }
}

function isPlayerHost(u) {
  try {
    const host = new URL(u).hostname.toLowerCase().replace(/^www\./, '');
    return PLAYER_HOSTS.some((h) => host === h || host.endsWith('.' + h));
  } catch (e) {
    return false;
  }
}

function videoFolder(u) {
  try {
    const x = new URL(u);
    return x.host + path.posix.dirname(x.pathname);
  } catch (e) {
    return u;
  }
}

function resolveAgainst(base, ref) {
  if (!ref) return '';
  try {
    return new URL(ref, base).href;
  } catch (e) {
    return '';
  }
}

function hasFfmpeg() {
  try {
    return spawnSync('ffmpeg', ['-version'], { stdio: 'ignore' }).status === 0;
  } catch (e) {
    return false;
  }
}

// ---------------------------------------------------------------------------
// HLS, as adplatform-v2's video.go reads it
// ---------------------------------------------------------------------------

function parseAttributes(line) {
  const out = {};
  const re = /([A-Z0-9-]+)=("[^"]*"|[^,]*)/gi;
  let m;
  while ((m = re.exec(line))) out[m[1].toUpperCase()] = m[2].replace(/^"|"$/g, '');
  return out;
}

// The variants a master playlist offers, widest first. Empty when it is
// already a media playlist.
function variants(text, base) {
  const lines = text.split(/\r?\n/);
  const out = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line.startsWith('#EXT-X-STREAM-INF')) continue;
    const a = parseAttributes(line.slice(line.indexOf(':') + 1));
    for (let j = i + 1; j < lines.length; j++) {
      const c = lines[j].trim();
      if (!c || c.startsWith('#')) continue;
      out.push({
        url: resolveAgainst(base, c),
        bandwidth: Number(a.BANDWIDTH || 0),
        average: Number(a['AVERAGE-BANDWIDTH'] || 0),
        resolution: a.RESOLUTION || '',
      });
      break;
    }
  }
  return out.sort((x, y) => y.bandwidth - x.bandwidth);
}

// The widest bandwidth a master playlist offers, or '' when it is already a
// media playlist.
function bestVariant(text, base) {
  const v = variants(text, base);
  return v.length ? v[0].url : '';
}

function durationOf(text) {
  let s = 0;
  for (const m of text.matchAll(/#EXTINF:\s*([0-9.]+)/g)) s += Number(m[1]) || 0;
  return s;
}

function parseMediaPlaylist(text, base) {
  const out = { segments: [], initURI: '', keyURI: '', iv: null, sequence: 0, fragmented: false };
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    if (line.startsWith('#EXT-X-KEY:')) {
      const a = parseAttributes(line.slice(11));
      if ((a.METHOD || '').toUpperCase() === 'NONE') {
        out.keyURI = '';
        out.iv = null;
        continue;
      }
      out.keyURI = resolveAgainst(base, a.URI);
      out.iv = a.IV ? Buffer.from(a.IV.replace(/^0x/i, ''), 'hex') : null;
    } else if (line.startsWith('#EXT-X-MAP:')) {
      out.initURI = resolveAgainst(base, parseAttributes(line.slice(11)).URI);
      out.fragmented = true;
    } else if (line.startsWith('#EXT-X-MEDIA-SEQUENCE:')) {
      out.sequence = Number(line.slice(22)) || 0;
    } else if (!line.startsWith('#')) {
      out.segments.push(resolveAgainst(base, line));
    }
  }
  if (!out.fragmented && out.segments.some((s) => /\.m4s(\?|$)/i.test(s))) out.fragmented = true;
  return out;
}

function decryptSegment(body, key, iv, sequence) {
  const crypto = require('node:crypto');
  let initial = iv;
  if (!initial || initial.length !== 16) {
    initial = Buffer.alloc(16);
    initial.writeBigUInt64BE(BigInt(sequence), 8);
  }
  const d = crypto.createDecipheriv('aes-128-cbc', key, initial);
  return Buffer.concat([d.update(body), d.final()]);
}

// ---------------------------------------------------------------------------
// One keep
// ---------------------------------------------------------------------------

// Waiting for response bodies is bounded: a stream or a long poll never ends,
// and waiting on it held a keep for over 7 minutes on 2026-09-27.
function settleBodies(pending, ms) {
  return Promise.race([Promise.allSettled([...pending]), new Promise((r) => setTimeout(r, ms))]);
}

function normalizeKeep(req) {
  const r = req || {};
  const num = (v, d) => (Number.isFinite(Number(v)) && Number(v) > 0 ? Number(v) : d);
  return {
    url: typeof r.url === 'string' ? r.url.trim() : '',
    html: typeof r.html === 'string' ? r.html : '',
    referer: typeof r.referer === 'string' && r.referer ? r.referer : undefined,
    proxy: r.proxy && r.proxy.server ? r.proxy : null,
    device: r.device === 'phone' ? 'phone' : 'desktop',
    timezone: typeof r.timezone === 'string' && r.timezone ? r.timezone : 'America/Chicago',
    dir: typeof r.dir === 'string' ? r.dir : '',
    dwellMs: num(r.dwellMs, 10000),
    maxFileBytes: num(r.maxFileBytes, DEFAULT_MAX_FILE_BYTES),
    maxVideoBytes: num(r.maxVideoBytes, DEFAULT_MAX_VIDEO_BYTES),
    maxSegments: num(r.maxSegments, DEFAULT_MAX_SEGMENTS),
    maxVideos: num(r.maxVideos, DEFAULT_MAX_VIDEOS),
  };
}

async function runKeep(body, deps, holder = {}) {
  const opts = normalizeKeep(body);
  const started = Date.now();
  opts.deadline = started + KEEP_BUDGET_MS;
  const state = {
    bytes: 0,
    files: [],
    byUrl: new Map(),
    pending: new Set(),
    playlists: [],
    media: [],
    n: 0,
    notes: [],
  };
  const answer = (ok, error, extra) => ({
    ok,
    error: error || null,
    bytes: state.bytes,
    durationMs: Date.now() - started,
    files: state.files,
    notes: state.notes,
    ...(extra || {}),
  });
  if (!opts.url || !opts.html) return answer(false, 'no url or no html given');
  if (!opts.dir || !path.isAbsolute(opts.dir)) return answer(false, 'no absolute directory given');
  fs.mkdirSync(opts.dir, { recursive: true });

  let entry = null;
  let context = null;
  try {
    entry = await deps.acquireBrowser(opts.proxy);
    if (holder.gone) return answer(false, 'the engine hung up');
    const base = opts.device === 'phone' ? deps.devices['iPhone 15'] : deps.DESKTOP;
    context = await entry.browser.newContext({
      ...base,
      locale: 'en-US',
      timezoneId: opts.timezone,
      ignoreHTTPSErrors: true,
    });
    // The alarm in keepWithTimeout closes it, so a keep that runs out of time
    // stops loading instead of running on unseen.
    holder.context = context;
    context.setDefaultNavigationTimeout(NAV_TIMEOUT_MS);
    context.setDefaultTimeout(20000);
    await context.addInitScript(deps.hideAutomation);

    let served = false;
    await context.route('**/*', (route) => {
      const req = route.request();
      const url = req.url();
      if (req.isNavigationRequest() && req.frame().parentFrame() === null) {
        // The stored copy stands in for the page itself. A later navigation of
        // the top frame (a refresh, a script redirect) would leave the page.
        if (!served) {
          served = true;
          return route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: opts.html });
        }
        return route.abort();
      }
      const type = req.resourceType();
      if (type === 'media' || (SEGMENT_RE.test(url) && (type === 'fetch' || type === 'xhr' || type === 'other'))) {
        state.media.push(url);
        return route.abort();
      }
      return route.continue();
    });

    context.on('response', (res) => {
      if (state.closed) return;
      const done = keepResponse(res, state, opts)
        .catch(() => {})
        .finally(() => state.pending.delete(done));
      state.pending.add(done);
    });

    const page = await context.newPage();
    page.on('dialog', (d) => d.dismiss().catch(() => {}));
    await page.goto(opts.url, { referer: opts.referer, waitUntil: 'domcontentloaded', timeout: NAV_TIMEOUT_MS });
    await page.waitForLoadState('networkidle', { timeout: SETTLE_MS }).catch(() => {});

    // Down the page in steps, so every picture that waits to be scrolled to
    // loads, and every video is started muted, so its player names its stream.
    await readThrough(page, opts.dwellMs);
    await page.waitForLoadState('networkidle', { timeout: SETTLE_MS }).catch(() => {});
    await settleBodies(state.pending, 15000);

    const renderedHtml = await page.evaluate(KEPT_PAGE_SCRIPT).catch(() => '');
    const title = await page.title().catch(() => '');
    const sources = await page.evaluate(VIDEO_SOURCES_SCRIPT).catch(() => []);
    const inlineCss = await page
      .evaluate(() => [...document.querySelectorAll('style')].map((s) => s.textContent || '').join('\n'))
      .catch(() => '');
    const pageUrl = page.url();

    // Everything left is fetched through the context, not the page. A page
    // left open keeps loading and refreshing its ads: two such pages took the
    // runner past 8 GB on 2026-09-27.
    state.closed = true;
    await page.close().catch(() => {});

    await followStylesheets(context, inlineCss, pageUrl, state, opts);

    const videos = await keepVideos(context, state, opts, sources);
    await settleBodies(state.pending, 15000);
    return answer(true, null, { renderedHtml, title, videos });
  } catch (err) {
    return answer(false, deps.message(err));
  } finally {
    if (context) await context.close().catch(() => {});
    if (entry) deps.releaseBrowser(entry);
  }
}

async function keepResponse(res, state, opts) {
  const req = res.request();
  if (req.isNavigationRequest() && res.frame().parentFrame() === null) return;
  const url = res.url();
  const headers = res.headers();
  const mediaType = (headers['content-type'] || 'application/octet-stream').split(';')[0].trim();
  const role = roleOf(req.resourceType(), url, mediaType);
  const status = res.status();
  if (status >= 300 && status < 400) return;
  let body = null;
  try {
    body = await res.body();
  } catch (e) {
    body = null;
  }
  state.bytes += body ? body.length : Number(headers['content-length'] || 0);
  if (!role) return;
  if (role === 'playlist') state.playlists.push(url);
  if (state.byUrl.has(url)) return;
  if (status >= 400) {
    remember(state, { url, role, mediaType, skippedReason: `the server answered ${status}` });
    return;
  }
  if (!body) {
    remember(state, { url, role, mediaType, skippedReason: 'no body returned' });
    return;
  }
  writeFile(state, opts, { url, role, mediaType }, body);
}

function remember(state, file) {
  state.byUrl.set(file.url, file);
  state.files.push(file);
}

function writeFile(state, opts, file, body) {
  if (body.length > opts.maxFileBytes) {
    remember(state, { ...file, size: body.length, skippedReason: `larger than the ${opts.maxFileBytes} byte limit` });
    return;
  }
  const name = `f${++state.n}`;
  const p = path.join(opts.dir, name);
  fs.writeFileSync(p, body);
  remember(state, { ...file, path: p, size: body.length });
}

async function readThrough(page, dwellMs) {
  const steps = 12;
  const slice = Math.max(250, Math.floor(dwellMs / steps));
  for (let i = 1; i <= steps; i++) {
    await page
      .evaluate((f) => {
        const h = document.body ? document.body.scrollHeight : 0;
        window.scrollTo({ top: h * f });
      }, i / steps)
      .catch(() => {});
    if (i === 2) {
      await page
        .evaluate(() => {
          const roots = [document];
          for (const el of document.querySelectorAll('*')) if (el.shadowRoot) roots.push(el.shadowRoot);
          for (const r of roots)
            for (const v of r.querySelectorAll('video')) {
              try {
                v.muted = true;
                const p = v.play();
                if (p && p.catch) p.catch(() => {});
              } catch (e) {}
            }
        })
        .catch(() => {});
    }
    await new Promise((r) => setTimeout(r, slice));
  }
  await page.evaluate(() => window.scrollTo({ top: 0 })).catch(() => {});
}

const CSS_URL_RE = /url\(\s*(['"]?)([^'")]+)\1\s*\)|@import\s+(['"])([^'"]+)\3/gi;

// The files a stylesheet names that the page did not happen to load: another
// breakpoint's background, a font weight nobody used yet. Opened offline
// months later, a copy is complete only with them.
async function followStylesheets(context, inlineCss, pageUrl, state, opts) {
  const wanted = [];
  const seen = new Set();
  const collect = (css, base) => {
    let m;
    CSS_URL_RE.lastIndex = 0;
    while ((m = CSS_URL_RE.exec(css))) {
      const ref = (m[2] || m[4] || '').trim();
      if (!ref || /^(data:|#|about:|blob:)/i.test(ref)) continue;
      const abs = withoutFragment(resolveAgainst(base, ref));
      if (!/^https?:/i.test(abs) || state.byUrl.has(abs) || seen.has(abs)) continue;
      seen.add(abs);
      wanted.push({ url: abs, referer: base, isCss: !!m[4] || /\.css(\?|$)/i.test(abs) });
    }
  };
  for (const f of [...state.files]) {
    if (f.role === 'stylesheet' && f.path) collect(fs.readFileSync(f.path, 'utf8'), f.url);
  }
  collect(inlineCss, pageUrl);

  // Eight at a time, and not past the page's 10 minutes: 200 files one by one
  // at 20 seconds each could take over an hour.
  const one = async (w) => {
    try {
      const res = await context.request.get(w.url, { headers: { referer: w.referer }, timeout: 20000 });
      const body = await res.body();
      state.bytes += body.length;
      const mediaType = (res.headers()['content-type'] || 'application/octet-stream').split(';')[0].trim();
      if (!res.ok()) {
        remember(state, { url: w.url, role: 'other', mediaType, skippedReason: `the server answered ${res.status()}` });
        return;
      }
      const role = roleOf('', w.url, mediaType) || (w.isCss ? 'stylesheet' : 'other');
      writeFile(state, opts, { url: w.url, role, mediaType }, body);
      if (role === 'stylesheet') collect(body.toString('utf8'), w.url);
    } catch (e) {
      remember(state, { url: w.url, role: 'other', mediaType: '', skippedReason: 'could not be fetched' });
    }
  };
  for (let i = 0; i < wanted.length && i < MAX_CSS_FOLLOW; i += 8) {
    if (Date.now() > opts.deadline) {
      state.notes.push(`stylesheet files not followed past ${i}: the 10 minute limit for one page`);
      break;
    }
    await Promise.all(wanted.slice(i, Math.min(i + 8, MAX_CSS_FOLLOW)).map(one));
  }
}

// The video of the page. Three cases, as in adplatform-v2: a playlist is
// stitched at its widest variant into one mp4, a plain file is downloaded
// whole, and a player that serves behind signed addresses (YouTube, Vimeo) is
// recorded by its address only. One video per folder: a master playlist and
// the variant beside it are one video.
async function keepVideos(context, state, opts, sources) {
  const out = [];
  const addresses = [];
  const seen = new Set();
  const add = (u) => {
    const a = withoutFragment(u);
    if (!a || !/^https?:/i.test(a) || seen.has(a)) return;
    seen.add(a);
    addresses.push(a);
  };
  // Playlists first: they are what a player really streamed.
  for (const u of state.playlists) add(u);
  for (const u of sources || []) add(u);
  for (const u of state.media) if (!SEGMENT_RE.test(u) || /\.(mp4|webm|mov)(\?|$)/i.test(u)) add(u);

  const stitched = new Set();
  for (const address of addresses) {
    if (out.filter((v) => v.path).length >= opts.maxVideos) break;
    if (Date.now() > opts.deadline) {
      out.push({ url: address, kind: 'address', note: 'not downloaded: the 10 minute limit for one page ran out' });
      continue;
    }
    if (PLAYLIST_RE.test(address)) {
      const folder = videoFolder(address);
      if (stitched.has(folder)) continue;
      stitched.add(folder);
      out.push(await stitch(context, state, opts, address));
      continue;
    }
    if (isPlayerHost(address)) {
      out.push({ url: address, kind: 'address', note: 'a player that serves its video behind signed addresses: address kept, no bytes' });
      continue;
    }
    if (/\.(ts|m4s|m4a|aac)(\?|$)/i.test(address)) continue; // a loose piece of a stream
    out.push(downloadWhole(address));
  }
  return out;
}

async function getThroughTheLine(context, state, url, referer, maxBytes) {
  const res = await context.request.get(url, {
    headers: { referer },
    timeout: 120000,
    maxRedirects: 10,
  });
  const len = Number(res.headers()['content-length'] || 0);
  if (maxBytes && len > maxBytes) {
    await res.dispose().catch(() => {});
    return { ok: false, tooLarge: len };
  }
  const body = await res.body();
  state.bytes += body.length;
  return { ok: res.ok(), status: res.status(), body, mediaType: (res.headers()['content-type'] || '').split(';')[0].trim() };
}

// A plain video file is named, not fetched: the engine downloads it whole
// through the line, straight to disk.
function downloadWhole(address) {
  return { url: address, kind: 'file', download: true };
}

async function stitch(context, state, opts, address) {
  const video = { url: address, kind: 'stitched' };
  if (/\.mpd(\?|$)/i.test(address)) return { ...video, note: 'a DASH manifest is not stitched: address kept' };
  try {
    const master = await getThroughTheLine(context, state, address, opts.url);
    if (!master.ok) return { ...video, note: `the playlist answered ${master.status}` };
    let playlistURL = address;
    let playlist = master.body.toString('utf8');
    // The widest variant whose expected size fits the limit: a sixty minute
    // sales video at its widest can be larger than one video may take, and a
    // whole video at a narrower width is worth more than half of one.
    const options = variants(playlist, address);
    for (let i = 0; i < options.length; i++) {
      const o = options[i];
      const v = await getThroughTheLine(context, state, o.url, opts.url);
      if (!v.ok) {
        if (i === options.length - 1) return { ...video, note: `the variant playlist answered ${v.status}` };
        continue;
      }
      const text = v.body.toString('utf8');
      const expected = ((o.average || o.bandwidth) * durationOf(text)) / 8;
      playlistURL = o.url;
      playlist = text;
      video.variant = o.url + (o.resolution ? ` (${o.resolution})` : '');
      video.seconds = Math.round(durationOf(text));
      if (expected <= opts.maxVideoBytes * 0.9 || i === options.length - 1) break;
    }
    const media = parseMediaPlaylist(playlist, playlistURL);
    if (media.segments.length === 0) return { ...video, note: 'the playlist names no pieces' };

    let key = null;
    if (media.keyURI) {
      const k = await getThroughTheLine(context, state, media.keyURI, opts.url);
      if (!k.ok || k.body.length !== 16) return { ...video, note: 'encrypted, and the key could not be read' };
      key = k.body;
    }

    const joinedPath = path.join(opts.dir, `joined${++state.n}.bin`);
    const fd = fs.openSync(joinedPath, 'w');
    let written = 0;
    let pieces = 0;
    let failed = 0;
    try {
      if (media.initURI) {
        const init = await getThroughTheLine(context, state, media.initURI, opts.url);
        if (init.ok) {
          fs.writeSync(fd, init.body);
          written += init.body.length;
        }
      }
      // Eight pieces at a time, written in order.
      const PARALLEL = 8;
      const fetchPiece = async (i) => {
        for (let attempt = 0; attempt < 3; attempt++) {
          try {
            const s = await getThroughTheLine(context, state, media.segments[i], opts.url);
            if (s.ok) return s.body;
          } catch (e) {}
        }
        return null;
      };
      const limit = Math.min(media.segments.length, opts.maxSegments);
      for (let i = 0; i < limit; i += PARALLEL) {
        if (written > opts.maxVideoBytes) {
          video.cut = `cut at piece ${i} of ${media.segments.length}: the ${opts.maxVideoBytes} byte limit`;
          break;
        }
        if (Date.now() > opts.deadline) {
          video.cut = `cut at piece ${i} of ${media.segments.length}: the 10 minute limit for one page`;
          break;
        }
        const batch = [];
        for (let j = i; j < Math.min(i + PARALLEL, limit); j++) batch.push(fetchPiece(j));
        const bodies = await Promise.all(batch);
        for (let k = 0; k < bodies.length; k++) {
          let seg = bodies[k];
          if (!seg) {
            failed++;
            continue;
          }
          if (key) {
            try {
              seg = decryptSegment(seg, key, media.iv, media.sequence + i + k);
            } catch (e) {
              failed++;
              continue;
            }
          }
          fs.writeSync(fd, seg);
          written += seg.length;
          pieces++;
        }
      }
      if (!video.cut && media.segments.length > opts.maxSegments) {
        video.cut = `cut at piece ${opts.maxSegments} of ${media.segments.length}: the piece limit`;
      }
    } finally {
      fs.closeSync(fd);
    }
    video.pieces = pieces;
    video.piecesTotal = media.segments.length;
    video.piecesFailed = failed;
    if (written === 0) return { ...video, note: 'no piece could be downloaded' };

    // One mp4 that plays anywhere, streams copied, not re-encoded. Without
    // ffmpeg the joined pieces are kept as they are (VLC plays them).
    const mp4 = path.join(opts.dir, `video${++state.n}.mp4`);
    if (hasFfmpeg()) {
      const r = spawnSync('ffmpeg', ['-loglevel', 'error', '-y', '-i', joinedPath, '-c', 'copy', '-movflags', '+faststart', mp4], {
        stdio: 'ignore',
        timeout: 2 * 60 * 1000,
      });
      if (r.status === 0 && fs.existsSync(mp4) && fs.statSync(mp4).size > 0) {
        fs.unlinkSync(joinedPath);
        return { ...video, path: mp4, size: fs.statSync(mp4).size, mediaType: 'video/mp4' };
      }
      video.note = 'ffmpeg could not remux the pieces: kept as joined';
    } else {
      video.note = 'no ffmpeg on this machine: the pieces are kept joined';
    }
    return { ...video, path: joinedPath, size: written, mediaType: media.fragmented ? 'video/mp4' : 'video/mp2t' };
  } catch (e) {
    return { ...video, note: 'could not be stitched' };
  }
}

// The video sources the page is showing: a <video>'s own source, its
// <source> children, in shadow roots too (vturb keeps its <video> in one), and
// the frames of the players known by name.
const VIDEO_SOURCES_SCRIPT = `() => {
  const out = [];
  const push = s => { if (s) { try { out.push(new URL(s, document.baseURI).href); } catch (e) {} } };
  const roots = [document];
  for (const el of document.querySelectorAll('*')) if (el.shadowRoot) roots.push(el.shadowRoot);
  for (const r of roots) {
    for (const v of r.querySelectorAll('video')) {
      push(v.currentSrc || v.getAttribute('src'));
      for (const s of v.querySelectorAll('source')) push(s.getAttribute('src'));
    }
  }
  for (const f of document.querySelectorAll('iframe')) {
    const src = f.getAttribute('src') || '';
    if (/youtube|youtube-nocookie|youtu\\.be|vimeo|wistia|vidalytics|jwplayer|brightcove|bunnycdn|mediadelivery|vturb|smartplayer|converteai|pandavideo|\\.m3u8|\\.mpd/i.test(src)) push(src);
  }
  return out.filter(u => /^https?:/i.test(u));
}`;

// The page as the browser left it, on a clone so the live page is untouched:
// every picture pointing at the file the browser really chose, every link
// absolute. Stylesheets stay <link>s; the files they are kept under are
// rewritten into the copy when it is opened.
const KEPT_PAGE_SCRIPT = `() => {
  const root = document.documentElement.cloneNode(true);
  const live = document.querySelectorAll('img');
  const copies = root.querySelectorAll('img');
  for (let i = 0; i < copies.length; i++) {
    let src = (live[i] && live[i].currentSrc) || copies[i].getAttribute('src') || '';
    if (!src || src.startsWith('data:')) continue;
    try { src = new URL(src, document.baseURI).href; } catch (e) {}
    copies[i].setAttribute('src', src);
  }
  for (const a of Array.from(root.querySelectorAll('a[href]'))) {
    try { a.setAttribute('href', new URL(a.getAttribute('href'), document.baseURI).href); } catch (e) {}
  }
  return '<!DOCTYPE html>\\n' + root.outerHTML;
}`;

function keepWithTimeout(body, deps) {
  let timer = null;
  const holder = {};
  const alarm = new Promise((resolve) => {
    timer = setTimeout(() => {
      // Stop the keep that ran out of time, or it goes on loading unseen.
      if (holder.context) holder.context.close().catch(() => {});
      resolve({ ok: false, error: 'keep timed out', bytes: 0, files: [], notes: [] });
    }, KEEP_TIMEOUT_MS);
  });
  return Promise.race([runKeep(body, deps, holder), alarm]).finally(() => clearTimeout(timer));
}

module.exports = { runKeep: keepWithTimeout, bestVariant, variants, durationOf, parseMediaPlaylist, roleOf };

'use strict';

// Raposa browser runner.
//
// POST /visit loads a link in headless Chromium under one disguise and
// reports what it found: the pages of the dark funnel, their HTML and text.
// The files a page loads are counted, not kept: the keeper (POST /keep, see
// keep.js) keeps each version whole once, afterwards. raposa-engine drives
// both over 127.0.0.1:8086 and produces the same step shape for its own plain
// HTTP visits, so one capture path stores both.
//
// Ported from adhunters-collector e20148c (browser/runner.js). Words follow
// GLOSSARY.md: investigation, disguise, rung, visit, white page, dark page,
// dark funnel, capture, keep, variant, live link.

const fs = require('node:fs');
const http = require('node:http');
const net = require('node:net');
const { chromium, devices } = require('playwright-core');
// POST /keep: one version of one page kept whole, files and video. See keep.js.
const keep = require('./keep');

const HOST = process.env.RAPOSA_BROWSER_HOST || '127.0.0.1';
const PORT = Number(process.env.RAPOSA_BROWSER_PORT || 8086);
// The Chromium build. Without RAPOSA_CHROME, the one playwright-core 1.63.0
// installs (npx playwright-core install chromium).
const CHROME = process.env.RAPOSA_CHROME || chromium.executablePath();
// Where the exit address is read from, through the visit's own line.
const IP_URL = process.env.RAPOSA_IP_URL || 'https://api.ipify.org';

const NAV_TIMEOUT_MS = Number(process.env.RAPOSA_NAV_TIMEOUT_MS || 45000);
// The Go client waits 170 seconds for an answer, so a visit gives up well
// before that and reports the steps and the bytes it already has.
const VISIT_TIMEOUT_MS = Number(process.env.RAPOSA_VISIT_TIMEOUT_MS || 110000);
const SETTLE_TIMEOUT_MS = 8000; // waiting for the network to go quiet
const CLICK_WAIT_MS = 20000; // waiting for a call to action to take us somewhere
const IP_TIMEOUT_MS = 20000;
const BROWSER_IDLE_MS = 5 * 60 * 1000; // a browser nobody uses is closed
const MAX_REQUEST_BYTES = 1 << 20; // a /visit request is small

// How much runs at once: two visits and one keep. More Chromium than that got
// the old runner killed for memory four times in an hour on 2026-09-27, and
// every visit in flight failed with it. A request past the limit is answered
// 503 at once, and the engine puts its investigation back and comes again.
// The engine holds the same limits (internal/engine/browser.go).
const VISIT_SLOTS = 2;
const KEEP_SLOTS = 1;

// Chromium grows over hours of pages, whatever is closed. After this many
// visits and keeps the runner stops taking work, lets what is running finish,
// and exits; systemd starts a fresh one.
const RESTART_AFTER = Number(process.env.RAPOSA_BROWSER_RESTART_AFTER || 200);

// What one visit may transfer before only the pages themselves still load. A
// rung with load_assets on meets autoplaying video, and nothing else caps what
// a browser pulls down, so this is what stands between a video sales letter and
// the metered home line.
const MAX_VISIT_BYTES = Number(process.env.RAPOSA_MAX_VISIT_BYTES || 67108864);

const DEFAULT_DWELL_MS = 8000;
const DEFAULT_MAX_STEPS = 6;

// The desktop disguise. Chrome 153 is what the bundled Chromium is, so the
// user agent says what the engine actually is. The phone disguise uses the
// iPhone 15 descriptor that ships with playwright.
const DESKTOP = {
  userAgent:
    'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36',
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 1,
  isMobile: false,
  hasTouch: false,
};

// Hosts that mean the funnel has reached checkout and there is nothing left to
// follow. Same platforms the Go side detects in internal/funnel/checkout.go.
const CHECKOUT_HOSTS = [
  'buygoods.com',
  'clickbank.net',
  'jvzoo.com',
  'digistore24.com',
  'checkout.stripe.com',
  'checkoutchamp.com',
  'konnektive.com',
  'pay.hotmart.com',
  'securecheckout',
];

const startedAt = Date.now();
let visitsRunning = 0;
let keepsRunning = 0;
let done = 0; // visits and keeps, for RESTART_AFTER
let draining = false;

function log(msg) {
  console.log(`[RAPOSA-BROWSER] ${msg}`);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// A playwright error carries a coloured call log under its first line. The
// engine stores this text on the visit, so it comes back plain and short.
function message(err) {
  const raw = err && err.message ? String(err.message) : String(err);
  return raw
    .replace(/\u001b\[[0-9;]*m/g, '')
    .split('\n')[0]
    .trim()
    .slice(0, 500);
}

function clamp(n, low, high) {
  return Math.min(high, Math.max(low, n));
}

function num(value, fallback) {
  const n = Number(value);
  return Number.isFinite(n) ? n : fallback;
}

function withTimeout(promise, ms, label) {
  let timer = null;
  const alarm = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(label)), ms);
  });
  return Promise.race([promise, alarm]).finally(() => clearTimeout(timer));
}

// Polls until the test passes or the clock runs out.
async function waitUntil(test, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (test()) return true;
    await sleep(150);
  }
  return test();
}

// ---------------------------------------------------------------------------
// Browsers, one per line
// ---------------------------------------------------------------------------

// One browser per proxy, reused across visits. The context is never reused, so
// cookies do not leak from one visit to the next.
const pool = new Map();

function proxyKey(proxy) {
  if (!proxy || !proxy.server) return 'direct';
  return [proxy.server, proxy.username || '', proxy.password || ''].join('|');
}

// A local forward proxy that adds the line's credentials, so Chromium itself
// needs none. Handed a proxy with a password, Playwright answers the proxy's
// challenge by intercepting every request, and interception switches the
// cache off, which stamps "Cache-Control: no-cache" and "Pragma: no-cache" on
// every request the page makes. No reader's browser sends those on a page
// load; everviewjournal.com served its white page to any request carrying
// them (2026-09-26).
function startRelay(upstream) {
  const up = new URL(upstream.server);
  const auth = 'Basic ' + Buffer.from(`${upstream.username || ''}:${upstream.password || ''}`).toString('base64');
  const server = http.createServer((req, res) => {
    // Plain http:// requests: forwarded as the absolute address they came as.
    const fwd = http.request(
      { host: up.hostname, port: Number(up.port), method: req.method, path: req.url, headers: { ...req.headers, 'proxy-authorization': auth } },
      (r) => {
        res.writeHead(r.statusCode, r.headers);
        r.pipe(res);
      }
    );
    fwd.on('error', () => res.destroy());
    req.pipe(fwd);
  });
  server.on('connect', (req, client, head) => {
    const tunnel = net.connect(Number(up.port), up.hostname, () => {
      tunnel.write(`CONNECT ${req.url} HTTP/1.1\r\nHost: ${req.url}\r\nProxy-Authorization: ${auth}\r\n\r\n`);
    });
    let buf = Buffer.alloc(0);
    const onData = (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      const end = buf.indexOf('\r\n\r\n');
      if (end < 0) return;
      tunnel.off('data', onData);
      const status = buf.subarray(0, end).toString().split(' ')[1];
      if (status !== '200') {
        client.end(`HTTP/1.1 ${status || '502'} Proxy Error\r\n\r\n`);
        tunnel.destroy();
        return;
      }
      client.write('HTTP/1.1 200 Connection Established\r\n\r\n');
      const rest = buf.subarray(end + 4);
      if (rest.length) client.write(rest);
      if (head && head.length) tunnel.write(head);
      tunnel.pipe(client);
      client.pipe(tunnel);
    };
    tunnel.on('data', onData);
    tunnel.on('error', () => client.destroy());
    client.on('error', () => tunnel.destroy());
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => resolve(server));
  });
}

async function launchBrowser(proxy) {
  let relay = null;
  let launchProxy;
  if (proxy && proxy.server && (proxy.username || proxy.password)) {
    relay = await startRelay(proxy);
    launchProxy = { server: `http://127.0.0.1:${relay.address().port}` };
  } else if (proxy && proxy.server) {
    launchProxy = proxy;
  }
  let browser;
  try {
    browser = await chromium.launch({
      executablePath: CHROME,
      headless: true,
      proxy: launchProxy,
      args: [
        '--disable-blink-features=AutomationControlled',
        '--no-sandbox',
        '--disable-dev-shm-usage',
        '--disable-gpu',
        '--lang=en-US',
      ],
    });
  } catch (err) {
    if (relay) relay.close();
    throw err;
  }
  if (relay) browser.on('disconnected', () => relay.close());
  return browser;
}

async function acquireBrowser(proxy) {
  const key = proxyKey(proxy);
  let entry = pool.get(key);
  if (entry && entry.browser && !entry.browser.isConnected()) {
    pool.delete(key);
    entry = null;
  }
  if (!entry) {
    entry = { key, browser: null, inUse: 0, idleSince: Date.now() };
    entry.ready = launchBrowser(proxy).then((browser) => {
      entry.browser = browser;
    });
    pool.set(key, entry);
    log(`launched a browser for ${key === 'direct' ? 'the direct line' : key.split('|')[0]}`);
  }
  entry.inUse++;
  try {
    await entry.ready;
  } catch (err) {
    releaseBrowser(entry);
    pool.delete(key);
    throw err;
  }
  return entry;
}

function releaseBrowser(entry) {
  entry.inUse--;
  entry.idleSince = Date.now();
}

setInterval(() => {
  for (const [key, entry] of pool) {
    if (entry.inUse > 0) continue;
    if (!entry.browser) continue;
    if (!entry.browser.isConnected()) {
      pool.delete(key);
      continue;
    }
    if (Date.now() - entry.idleSince < BROWSER_IDLE_MS) continue;
    pool.delete(key);
    entry.browser.close().catch(() => {});
  }
}, 60000).unref();

// ---------------------------------------------------------------------------
// One visit
// ---------------------------------------------------------------------------

function normalize(body) {
  const req = body || {};
  return {
    url: typeof req.url === 'string' ? req.url.trim() : '',
    referer: typeof req.referer === 'string' && req.referer ? req.referer : undefined,
    proxy: req.proxy && req.proxy.server ? req.proxy : null,
    device: req.device === 'phone' ? 'phone' : 'desktop',
    timezone: typeof req.timezone === 'string' && req.timezone ? req.timezone : 'America/Chicago',
    loadAssets: req.loadAssets === true,
    humanDwell: req.humanDwell === true,
    dwellMs: clamp(num(req.dwellMs, DEFAULT_DWELL_MS), 0, 120000),
    maxSteps: clamp(num(req.maxSteps, DEFAULT_MAX_STEPS), 0, 12),
  };
}

// Runs in the page before anything else. This is about looking like an
// ordinary browser, not about defeating anything clever.
function hideAutomation() {
  Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
  Object.defineProperty(navigator, 'languages', { get: () => ['en-US', 'en'] });
  Object.defineProperty(navigator, 'plugins', {
    get: () => [
      { name: 'PDF Viewer', filename: 'internal-pdf-viewer' },
      { name: 'Chrome PDF Viewer', filename: 'internal-pdf-viewer' },
      { name: 'Chromium PDF Viewer', filename: 'internal-pdf-viewer' },
    ],
  });
  if (!window.chrome) window.chrome = { runtime: {} };
}

async function openContext(browser, opts, state) {
  const base = opts.device === 'phone' ? devices['iPhone 15'] : DESKTOP;
  const context = await browser.newContext({
    ...base,
    // No locale option: Playwright's sends "Accept-Language: en-US" alone,
    // which no phone sends, and everviewjournal.com served its white page to
    // it. The browser runs with --lang=en-US and sends what Safari and Chrome
    // send.
    extraHTTPHeaders: { 'Accept-Language': 'en-US,en;q=0.9' },
    timezoneId: opts.timezone,
    ignoreHTTPSErrors: true,
  });
  context.setDefaultNavigationTimeout(NAV_TIMEOUT_MS);
  context.setDefaultTimeout(CLICK_WAIT_MS);
  await context.addInitScript(hideAutomation);
  // Requests are held through the DevTools protocol, page by page, not through
  // Playwright's routes: a route switches the cache off, and then every
  // request says "Cache-Control: no-cache" (see startRelay).
  context.on('page', (page) => {
    page.__intercepting = interceptPage(context, page, opts, state).catch(() => {});
  });
  return context;
}

// interceptPage blocks what the visit does not pay for, and serves Taboola's
// click page from memory (see landFromTaboolaClick).
async function interceptPage(context, page, opts, state) {
  const cdp = await context.newCDPSession(page);
  cdp.on('Fetch.requestPaused', (e) => {
    const type = e.resourceType;
    let answer;
    if (state.clickPage && isTaboolaClick(e.request.url) && type === 'Document') {
      answer = cdp.send('Fetch.fulfillRequest', {
        requestId: e.requestId,
        responseCode: 200,
        responseHeaders: [{ name: 'Content-Type', value: 'text/html; charset=utf-8' }],
        body: Buffer.from(state.clickPage).toString('base64'),
      });
    } else if (!opts.loadAssets && (type === 'Image' || type === 'Media' || type === 'Font')) {
      // A metered home line is not spent on video, images and fonts.
      answer = cdp.send('Fetch.failRequest', { requestId: e.requestId, errorReason: 'BlockedByClient' });
    } else if (state.bytes >= MAX_VISIT_BYTES && type !== 'Document') {
      // Past the ceiling only the pages themselves still load, so one long
      // video cannot run away with the metered line.
      answer = cdp.send('Fetch.failRequest', { requestId: e.requestId, errorReason: 'BlockedByClient' });
    } else {
      answer = cdp.send('Fetch.continueRequest', { requestId: e.requestId });
    }
    answer.catch(() => {});
  });
  await cdp.send('Fetch.enable', { patterns: [{ urlPattern: '*', requestStage: 'Request' }] });
}

function preparePage(page) {
  // A dialog left open wedges the runner, so every one is dismissed.
  page.on('dialog', (dialog) => dialog.dismiss().catch(() => {}));
}

// Every response is counted into bytes, so the engine knows what the metered
// line paid. Nothing else of a file is kept here; the keeper keeps the page.
function watchResponses(context, state) {
  context.on('response', (res) => {
    const done = recordResponse(res, state)
      .catch(() => {})
      .finally(() => state.pending.delete(done));
    state.pending.add(done);
  });
}

async function recordResponse(res, state) {
  const req = res.request();

  // A navigation of a top level frame is a hop: every redirect, then the page
  // that answers.
  if (req.isNavigationRequest() && res.frame().parentFrame() === null) {
    state.hops.push({ url: res.url(), status: res.status() });
  }

  let size = num(res.headers()['content-length'], 0);
  try {
    size = (await res.body()).length;
  } catch (err) {
    // A redirect, or a body the browser did not keep: what it declared.
  }
  state.bytes += size;
}

// The address this line exits from. A residential line in an avoided place
// sees the white page, so the engine needs to know where it came out.
async function readExitIp(context) {
  let page = null;
  try {
    page = await context.newPage();
    await page.goto(IP_URL, { waitUntil: 'domcontentloaded', timeout: IP_TIMEOUT_MS });
    const text = (await page.evaluate(() => (document.body ? document.body.innerText : ''))).trim();
    return /^[0-9a-f.:]{7,45}$/i.test(text) ? text : null;
  } catch (err) {
    return null;
  } finally {
    if (page) await page.close().catch(() => {});
  }
}

// Two waits, with two names: the old runner had both as settle(), the second
// replaced the first, and every browser visit failed with "not iterable".
async function settlePage(page) {
  await page.waitForLoadState('domcontentloaded', { timeout: NAV_TIMEOUT_MS }).catch(() => {});
  // These pages rarely go fully quiet, so this one is best effort.
  await page.waitForLoadState('networkidle', { timeout: SETTLE_TIMEOUT_MS }).catch(() => {});
}

// Some pages only reveal the offer after a reader behaves like one.
async function dwell(page, opts) {
  if (!opts.humanDwell) {
    await sleep(opts.dwellMs);
    return;
  }
  const rounds = 4;
  const slice = Math.max(400, Math.floor(opts.dwellMs / (rounds + 2)));
  for (let i = 1; i <= rounds; i++) {
    await page.mouse.move(120 + i * 90, 180 + i * 70).catch(() => {});
    await page
      .evaluate((fraction) => {
        const height = document.body ? document.body.scrollHeight : 0;
        window.scrollTo({ top: height * fraction, behavior: 'smooth' });
      }, i / (rounds + 1))
      .catch(() => {});
    await sleep(slice);
  }
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'smooth' })).catch(() => {});
  await sleep(slice);
}

// Waiting for response bodies is bounded: a stream or a long poll never ends,
// and a visit that waited on one ran out of its time with its pages unread.
function settleBodies(pending, ms) {
  return Promise.race([Promise.allSettled([...pending]), new Promise((r) => setTimeout(r, ms))]);
}

// One distinct page of the dark funnel, kept whole so it can be read back later.
async function captureStep(page, state, stepNo, reachedBy, clicked) {
  // The bodies of this step's responses are read before the page is read.
  await settleBodies(state.pending, 15000);
  const hops = state.hops.splice(0);
  const last = hops.length ? hops[hops.length - 1] : null;
  return {
    stepNo,
    reachedBy,
    clickedText: clicked && clicked.text ? clicked.text : null,
    clickedUrl: clicked && clicked.href ? clicked.href : null,
    url: page.url(),
    status: last ? last.status : null,
    title: await page.title().catch(() => ''),
    html: await page.content().catch(() => ''),
    text: await page.evaluate(() => (document.body ? document.body.innerText : '')).catch(() => ''),
    hops,
  };
}

// The funnel stops here: an operator's checkout is the last page there is.
async function looksLikeCheckout(page, step) {
  const url = (step.url || '').toLowerCase();
  if (CHECKOUT_HOSTS.some((host) => url.includes(host))) return true;
  return page
    .evaluate(
      () =>
        !!document.querySelector(
          'form input[autocomplete="cc-number"], form input[name*="card" i], form input[id*="card" i], form input[name*="cc_num" i], form input[placeholder*="card number" i]'
        )
    )
    .catch(() => false);
}

// The call to action the page most wants clicked. Runs in the page, and marks
// what it chose so the click can find it again.
function pickCta() {
  const SKIP = [
    'privacy',
    'terms',
    'contact',
    'disclaimer',
    'policies',
    'policy',
    'refund',
    'about',
    'cookie',
    'unsubscribe',
    'facebook.com',
    'twitter.com',
    'instagram.com',
    'ncbi.nlm.nih.gov',
  ];
  const WORDS = /\b(watch|continue|learn more|get|order|buy|claim|yes|next)\b/i;
  const CLICK_PATH = /\/click(\/\d*|\.php)?\/?$/i;
  const visible = (el) => el.getClientRects().length > 0;

  for (const marked of document.querySelectorAll('[data-raposa-cta]')) {
    marked.removeAttribute('data-raposa-cta');
  }

  const here = new URL(window.location.href);
  const byHref = new Map();
  for (const a of document.querySelectorAll('a[href]')) {
    let u = null;
    try {
      u = new URL(a.getAttribute('href'), window.location.href);
    } catch (err) {
      continue;
    }
    if (u.protocol !== 'http:' && u.protocol !== 'https:') continue;
    u.hash = '';
    // The page linking to itself is not a step.
    if (u.host === here.host && u.pathname === here.pathname) continue;
    const lower = (u.host + u.pathname).toLowerCase();
    if (SKIP.some((s) => lower.includes(s))) continue;

    const text = (a.innerText || a.textContent || '').trim().slice(0, 200);
    const found = byHref.get(u.href);
    if (found) {
      found.count++;
      if (text && !found.text) found.text = text;
      if (!visible(found.el) && visible(a)) found.el = a;
      continue;
    }
    byHref.set(u.href, { el: a, href: u.href, text, count: 1, path: u.pathname });
  }

  // An advertorial repeats its offer link, and a tracker click link is always
  // the offer button.
  let best = null;
  let bestScore = 0;
  for (const candidate of byHref.values()) {
    let score = candidate.count;
    if (WORDS.test(candidate.text)) score += 3;
    if (CLICK_PATH.test(candidate.path)) score += 2;
    if (score > bestScore) {
      best = candidate;
      bestScore = score;
    }
  }
  if (best && bestScore >= 2) {
    best.el.setAttribute('data-raposa-cta', '1');
    return { text: best.text, href: best.href };
  }

  // No link qualifies: a button whose text reads like a call to action. Those
  // pages navigate from a script.
  for (const b of document.querySelectorAll('button, [role="button"], input[type="submit"], input[type="button"]')) {
    const text = (b.innerText || b.value || '').trim().slice(0, 200);
    if (!text || !WORDS.test(text)) continue;
    if (!visible(b)) continue;
    b.setAttribute('data-raposa-cta', '1');
    return { text, href: null };
  }
  return null;
}

// Clicks what pickCta marked and returns the page the funnel goes on with, or
// null when the click led nowhere.
async function followCta(context, page, cta) {
  const before = page.url();
  let popup = null;
  const onPage = (opened) => {
    if (!popup) popup = opened;
  };
  context.on('page', onPage);
  try {
    await page.click('[data-raposa-cta]', { timeout: CLICK_WAIT_MS }).catch(() => {});
    await waitUntil(() => popup !== null || page.url() !== before, CLICK_WAIT_MS);
    if (popup) {
      preparePage(popup);
      // The page we left would keep loading on a metered line.
      await page.close().catch(() => {});
      return popup;
    }
    if (page.url() !== before) return page;
    // The click did nothing. When we know where it pointed, go there ourselves.
    if (cta.href) {
      await page.goto(cta.href, { referer: before, waitUntil: 'domcontentloaded', timeout: NAV_TIMEOUT_MS });
      return page;
    }
    return null;
  } catch (err) {
    return null;
  } finally {
    context.off('page', onPage);
  }
}

// A reader who clicks a Taboola card lands through Taboola's click page, whose
// script sends the browser on with location.replace under an unsafe-url
// referrer policy. The landing therefore sees Taboola's whole click address as
// Referer and no Sec-Fetch-User, since no click of the reader's started that
// load. everviewjournal.com served its dark page only to such a load
// (2026-09-26). page.goto cannot produce it, so the click page is served here,
// from memory, at Taboola's address, and does what Taboola's does. Nothing is
// sent to Taboola, so no click is charged to the operator.
function isTaboolaClick(referer) {
  try {
    const u = new URL(referer);
    return u.hostname === 'trc.taboola.com' && u.pathname.includes('/log/3/click');
  } catch (err) {
    return false;
  }
}

async function landFromTaboolaClick(page, opts, state) {
  const target = JSON.stringify(opts.url).replace(/</g, '\\u003c');
  state.clickPage =
    '<!DOCTYPE html><html><head><meta name="referrer" content="unsafe-url"></head>' +
    `<body><script>document.location.replace(${target});</script></body></html>`;
  const landed = page.waitForURL((u) => !isTaboolaClick(u.toString()), {
    waitUntil: 'domcontentloaded',
    timeout: NAV_TIMEOUT_MS,
  });
  await page.goto(opts.referer, { waitUntil: 'commit', timeout: NAV_TIMEOUT_MS });
  await landed;
}

// The landing page, then every step the funnel leads to, up to maxSteps past it.
async function walkFunnel(context, opts, state, steps) {
  let page = await context.newPage();
  preparePage(page);
  await page.__intercepting;
  if (isTaboolaClick(opts.referer)) {
    await landFromTaboolaClick(page, opts, state);
  } else {
    await page.goto(opts.url, {
      referer: opts.referer,
      waitUntil: 'domcontentloaded',
      timeout: NAV_TIMEOUT_MS,
    });
  }

  let reachedBy = 'landing';
  let clicked = null;
  for (let stepNo = 1; stepNo <= opts.maxSteps + 1; stepNo++) {
    await settlePage(page);
    await dwell(page, opts);
    const step = await captureStep(page, state, stepNo, reachedBy, clicked);
    steps.push(step);

    if (await looksLikeCheckout(page, step)) break;
    // A page that moves on by itself (a meta refresh, a script) is the next step.
    if (page.url() !== step.url) {
      reachedBy = 'redirect';
      clicked = null;
      continue;
    }
    // The last step this visit may keep has been captured. Clicking on from
    // here would load a page nothing reads, and on a metered line that page is
    // paid for all the same.
    if (stepNo > opts.maxSteps) break;
    const cta = await page.evaluate(pickCta).catch(() => null);
    if (!cta) break;
    const next = await followCta(context, page, cta);
    if (!next) break;
    page = next;
    reachedBy = 'cta';
    clicked = cta;
  }
}

function envelope(ok, error, state, steps, started, exitIp) {
  return {
    ok,
    bytes: state.bytes,
    durationMs: Date.now() - started,
    exitIp: exitIp || null,
    error: error || null,
    steps,
  };
}

// holder.context is set as soon as there is one, so a caller that hangs up
// can close it and the visit stops loading (see the server below).
async function runVisit(body, holder = {}) {
  const opts = normalize(body);
  const started = Date.now();
  const state = { bytes: 0, pending: new Set(), hops: [] };
  const steps = [];
  let exitIp = null;
  if (!opts.url) return envelope(false, 'no url given', state, steps, started, exitIp);

  let entry = null;
  let context = null;
  try {
    entry = await acquireBrowser(opts.proxy);
    if (holder.gone) return envelope(false, 'the engine hung up', state, steps, started, exitIp);
    context = await openContext(entry.browser, opts, state);
    holder.context = context;
    watchResponses(context, state);

    // The exit address is read first, so a visit that fails later still says
    // where the line came out. Its traffic counts, but it is not the funnel.
    exitIp = await readExitIp(context);
    await settleBodies(state.pending, 15000);
    state.hops.length = 0;

    // The budget covers the whole visit, the browser launch and the exit
    // address read included, not the funnel walk alone. Otherwise a cold
    // launch plus a slow exit address read can push the answer past the 120
    // seconds the Go client waits, and every byte the metered line spent goes
    // unaccounted.
    const left = VISIT_TIMEOUT_MS - (Date.now() - started);
    if (left <= 0) return envelope(false, 'visit timed out', state, steps, started, exitIp);
    await withTimeout(walkFunnel(context, opts, state, steps), left, 'visit timed out');
    return envelope(true, null, state, steps, started, exitIp);
  } catch (err) {
    return envelope(false, message(err), state, steps, started, exitIp);
  } finally {
    // The context always closes, so a failed visit leaves nothing running.
    if (context) await context.close().catch(() => {});
    if (entry) releaseBrowser(entry);
  }
}

// ---------------------------------------------------------------------------
// The server
// ---------------------------------------------------------------------------

function readBody(req, limit) {
  const max = limit || MAX_REQUEST_BYTES;
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on('data', (chunk) => {
      size += chunk.length;
      if (size > max) {
        reject(new Error('request too large'));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    req.on('error', reject);
  });
}

function sendJson(res, status, payload) {
  const body = Buffer.from(JSON.stringify(payload), 'utf8');
  res.writeHead(status, {
    'content-type': 'application/json; charset=utf-8',
    'content-length': body.length,
  });
  res.end(body);
}

// Takes a slot for one job, or answers 503 and returns false.
function admit(res, kind) {
  if (draining) {
    sendJson(res, 503, { ok: false, error: 'restarting', steps: [], files: [] });
    return false;
  }
  const full = kind === 'keep' ? keepsRunning >= KEEP_SLOTS : visitsRunning >= VISIT_SLOTS;
  if (full) {
    sendJson(res, 503, { ok: false, error: `no free ${kind} slot`, steps: [], files: [] });
    return false;
  }
  if (kind === 'keep') keepsRunning++;
  else visitsRunning++;
  return true;
}

// Gives the slot back, and past RESTART_AFTER jobs starts the drain.
function finished(kind) {
  if (kind === 'keep') keepsRunning--;
  else visitsRunning--;
  done++;
  if (!draining && done >= RESTART_AFTER) {
    draining = true;
    log(`${done} visits and keeps done: finishing what runs, then restarting`);
  }
  if (draining && visitsRunning === 0 && keepsRunning === 0) shutdown();
}

// When the engine hangs up (its own deadline, or it is stopping), the job's
// context is closed so the browser stops loading for nobody.
function onHangUp(req, res, holder) {
  res.on('close', () => {
    if (res.writableEnded) return;
    holder.gone = true;
    if (holder.context) holder.context.close().catch(() => {});
  });
}

const server = http.createServer(async (req, res) => {
  const path = (req.url || '').split('?')[0];

  if (req.method === 'GET' && path === '/healthz') {
    // Without the Chromium build there is no browser rung, and the runner
    // would answer every visit with the same launch failure. Saying so here
    // is what keeps a deployment from going green on a runner that cannot run.
    const chromeFound = fs.existsSync(CHROME);
    sendJson(res, chromeFound ? 200 : 503, {
      ok: chromeFound,
      error: chromeFound ? null : `no Chromium build at ${CHROME}`,
      browsers: pool.size,
      visitsRunning,
      keepsRunning,
      done,
      draining,
      uptimeSec: Math.round((Date.now() - startedAt) / 1000),
      chrome: CHROME,
    });
    return;
  }

  if (req.method === 'POST' && path === '/keep') {
    let keepBody = null;
    try {
      // A keep carries the stored HTML of the page, which can be megabytes.
      keepBody = JSON.parse(await readBody(req, 32 << 20));
    } catch (err) {
      sendJson(res, 400, { ok: false, error: `bad request: ${message(err)}`, files: [] });
      return;
    }
    if (!admit(res, 'keep')) return;
    log(`keep ${keepBody.device === 'phone' ? 'phone' : 'desktop'} ${keepBody.proxy ? 'proxied' : 'direct'} ${keepBody.url}`);
    const holder = {};
    onHangUp(req, res, holder);
    let out;
    try {
      out = await keep.runKeep(keepBody, { acquireBrowser, releaseBrowser, devices, DESKTOP, hideAutomation, message }, holder);
    } finally {
      finished('keep');
    }
    log(`keep ${out.ok ? 'done' : 'failed'}: files=${out.files.length} videos=${(out.videos || []).length} bytes=${out.bytes} ms=${out.durationMs}` +
      (out.error ? ` error=${out.error}` : ''));
    if (!holder.gone) sendJson(res, 200, out);
    return;
  }

  if (req.method !== 'POST' || path !== '/visit') {
    sendJson(res, 404, { ok: false, bytes: 0, durationMs: 0, exitIp: null, error: 'not found', steps: [] });
    return;
  }

  let body = null;
  try {
    body = JSON.parse(await readBody(req));
  } catch (err) {
    sendJson(res, 400, {
      ok: false,
      bytes: 0,
      durationMs: 0,
      exitIp: null,
      error: `bad request: ${message(err)}`,
      steps: [],
    });
    return;
  }
  if (!admit(res, 'visit')) return;

  const target = typeof body.url === 'string' ? body.url : '';
  log(`visit ${body.device === 'phone' ? 'phone' : 'desktop'} ${body.proxy ? 'proxied' : 'direct'} ${target}`);
  const holder = {};
  onHangUp(req, res, holder);
  let out;
  try {
    out = await runVisit(body, holder);
  } finally {
    finished('visit');
  }
  log(
    `visit ${out.ok ? 'done' : 'failed'}: steps=${out.steps.length} bytes=${out.bytes} ms=${out.durationMs}` +
      (out.error ? ` error=${out.error}` : '')
  );
  if (!holder.gone) sendJson(res, 200, out);
});

process.on('unhandledRejection', (err) => log(`unhandled rejection: ${message(err)}`));

// Stops well inside systemd's 30 seconds: the browsers close, and a job still
// running dies with them (the engine records nothing for it and comes again).
let stopping = false;
async function shutdown() {
  if (stopping) return;
  stopping = true;
  server.close();
  const timer = setTimeout(() => process.exit(0), 20000);
  timer.unref();
  for (const [, entry] of pool) {
    if (entry.browser) await entry.browser.close().catch(() => {});
  }
  process.exit(0);
}
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);

server.listen(PORT, HOST, () => log(`listening on http://${HOST}:${PORT}, chromium at ${CHROME}`));

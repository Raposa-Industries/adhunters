'use strict';

// Drives a real runner against a small cloaked site on this machine: a visit
// follows the funnel to the order page, the slots answer 503 when full, and a
// keep writes the page's files and names its video for the engine to fetch.
//
// Needs Chromium: RAPOSA_CHROME, or the build playwright-core installs. Skipped
// without one.  npm test

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const { spawn } = require('node:child_process');
const { chromium } = require('playwright-core');
const keep = require('./keep');

const CHROME = process.env.RAPOSA_CHROME || chromium.executablePath();
const haveChrome = fs.existsSync(CHROME);

// The cloaked site: a white page, unless the visit carries a click id, which
// sends it on to the offer, whose button leads to the order page.
function site() {
  const server = http.createServer((req, res) => {
    const u = new URL(req.url, 'http://x');
    const html = (title, body) => {
      res.writeHead(200, { 'content-type': 'text/html' });
      res.end(`<!DOCTYPE html><html><head><title>${title}</title><link rel="stylesheet" href="/s.css"></head><body>${body}</body></html>`);
    };
    switch (u.pathname) {
      case '/':
        if (u.searchParams.get('tblci')) {
          res.writeHead(302, { location: '/offer' });
          res.end();
          return;
        }
        return html('Ten garden tips', '<p>Water in the morning.</p>');
      case '/offer':
        return html('The secret', '<p>Watch this.</p><video src="/v.mp4"></video><a href="/order">Order now</a><a href="/order">Get it</a>');
      case '/order':
        return html('Order', '<form><input name="card_number"></form>');
      case '/slow':
        setTimeout(() => html('Slow', '<p>late</p>'), 4000);
        return;
      case '/s.css':
        res.writeHead(200, { 'content-type': 'text/css' });
        res.end('body { background: url(/bg.png) }');
        return;
      case '/bg.png':
        res.writeHead(200, { 'content-type': 'image/png' });
        res.end(Buffer.from('89504e470d0a1a0a', 'hex'));
        return;
      case '/v.mp4':
        res.writeHead(200, { 'content-type': 'video/mp4' });
        res.end(Buffer.alloc(1000));
        return;
    }
    res.writeHead(404);
    res.end();
  });
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server)));
}

async function startRunner(env) {
  const port = 20000 + Math.floor(Math.random() * 20000);
  const child = spawn(process.execPath, [path.join(__dirname, 'runner.js')], {
    env: { ...process.env, RAPOSA_BROWSER_PORT: String(port), RAPOSA_IP_URL: 'http://127.0.0.1:1/', ...env },
    stdio: ['ignore', 'pipe', 'inherit'],
  });
  await new Promise((resolve, reject) => {
    child.stdout.on('data', (d) => d.toString().includes('listening') && resolve());
    child.on('exit', (code) => reject(new Error(`runner exited ${code}`)));
  });
  return { child, addr: `http://127.0.0.1:${port}` };
}

async function post(addr, route, body) {
  const res = await fetch(addr + route, { method: 'POST', body: JSON.stringify(body), headers: { 'content-type': 'application/json' } });
  return { status: res.status, body: await res.json() };
}

test('the runner', { skip: !haveChrome && `no Chromium at ${CHROME}` }, async (t) => {
  const s = await site();
  const base = `http://127.0.0.1:${s.address().port}`;
  const { child, addr } = await startRunner({ RAPOSA_BROWSER_RESTART_AFTER: '1000' });
  t.after(() => {
    child.kill('SIGTERM');
    s.close();
  });

  await t.test('a visit follows the funnel to the order page', async () => {
    const { status, body } = await post(addr, '/visit', { url: `${base}/?tblci=abc`, referer: 'https://www.foxnews.com/', dwellMs: 200, maxSteps: 3 });
    assert.equal(status, 200);
    assert.equal(body.ok, true, body.error);
    assert.deepEqual(
      body.steps.map((x) => [x.reachedBy, new URL(x.url).pathname, x.title]),
      [
        ['landing', '/offer', 'The secret'],
        ['cta', '/order', 'Order'],
      ]
    );
    assert.equal(body.steps[0].hops[0].status, 302);
    assert.ok(body.bytes > 0);
    assert.equal(body.steps[0].assets, undefined, 'a visit keeps no files');
  });

  await t.test('without the click id the white page is all there is', async () => {
    const { body } = await post(addr, '/visit', { url: `${base}/`, dwellMs: 100, maxSteps: 3 });
    assert.equal(body.ok, true, body.error);
    assert.deepEqual(body.steps.map((x) => x.title), ['Ten garden tips']);
  });

  await t.test('a third visit at once is turned away', async () => {
    const slow = { url: `${base}/slow`, dwellMs: 100, maxSteps: 0 };
    const first = [post(addr, '/visit', slow), post(addr, '/visit', slow)];
    await new Promise((r) => setTimeout(r, 300));
    const third = await post(addr, '/visit', slow);
    assert.equal(third.status, 503);
    for (const r of await Promise.all(first)) assert.equal(r.status, 200);
  });

  await t.test('it serves its numbers', async () => {
    const text = await (await fetch(addr + '/metrics')).text();
    assert.match(text, /^raposa_browser_jobs_done_total [1-9]/m);
    assert.match(text, /^raposa_browser_visits_running 0$/m);
  });

  await t.test('a keep writes the files and names the video', async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'raposa-keep-'));
    t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
    const html = '<!DOCTYPE html><html><head><title>The secret</title><link rel="stylesheet" href="/s.css"></head><body><video src="/v.mp4"></video></body></html>';
    const { status, body } = await post(addr, '/keep', { url: `${base}/offer`, html, dir, dwellMs: 300 });
    assert.equal(status, 200);
    assert.equal(body.ok, true, body.error);
    const kept = body.files.filter((f) => f.path).map((f) => new URL(f.url).pathname).sort();
    assert.deepEqual(kept, ['/bg.png', '/s.css']);
    for (const f of body.files) if (f.path) assert.ok(f.path.startsWith(dir));
    assert.deepEqual(body.videos, [{ url: `${base}/v.mp4`, kind: 'file', download: true }]);
  });
});

test('a runner restarts itself after its visits', { skip: !haveChrome && `no Chromium at ${CHROME}` }, async (t) => {
  const s = await site();
  t.after(() => s.close());
  const base = `http://127.0.0.1:${s.address().port}`;
  const { child, addr } = await startRunner({ RAPOSA_BROWSER_RESTART_AFTER: '1' });
  const exited = new Promise((resolve) => child.on('exit', resolve));
  const { body } = await post(addr, '/visit', { url: `${base}/`, dwellMs: 100, maxSteps: 0 });
  assert.equal(body.ok, true, body.error);
  assert.equal(await exited, 0);
});

test('the playlists', () => {
  const master = '#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2000000\nhigh.m3u8\n';
  assert.equal(keep.bestVariant(master, 'https://cdn.example/v/master.m3u8'), 'https://cdn.example/v/high.m3u8');
  const media = keep.parseMediaPlaylist('#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:4.0,\na.ts\n#EXTINF:4.5,\nb.ts\n', 'https://cdn.example/v/high.m3u8');
  assert.deepEqual(media.segments, ['https://cdn.example/v/a.ts', 'https://cdn.example/v/b.ts']);
  assert.equal(media.sequence, 5);
  assert.equal(keep.durationOf('#EXTINF:4.0,\n#EXTINF:4.5,\n'), 8.5);
});

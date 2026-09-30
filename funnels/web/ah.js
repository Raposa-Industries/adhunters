/*
 * AdHunters Funnels page script: the beacon and the VSL player.
 *
 * One tag on a landing page, added by funnels-edge on the pages it hosts:
 *
 *   <script async src="/ah.js" data-site="lp.example.com" data-lp="bp-02"
 *           data-clarity="abcd1234"></script>
 *
 * It keeps one journey per tracker click id in a first-party cookie, and
 * sends what happens on the page to the collector (/e on the same host
 * unless data-endpoint says otherwise) in batches with navigator.sendBeacon:
 * the page view, visible time every 5 s, scroll marks, marked steps, clicks
 * and form starts, the exit, and the player's events and watched seconds.
 *
 * Markup the page author uses (no code):
 *   data-ah-step="offer"            a step, reached when half of it is in view
 *   data-ah-step-on="click"         ... or only when it is clicked
 *   data-ah-click="buy"             a click worth counting
 *   data-ah-form="lead"             a form (its first input counts as a start)
 *   data-ah-video="bp-02"           the player (see mountPlayer)
 *   data-show-at="14:00"            hidden until the player passes 14:00 (the
 *                                   edge's style tag hides it before this loads)
 *   href="...{clickid}..."          filled with the tracker click id
 *
 * Plain ES2017, no build step. The pure helpers are exported on
 * AdHuntersFunnels for the tests (node --test funnels/web/test/).
 */
(function (root) {
  "use strict";

  var SCHEMA = 1;
  var BEAT_MS = 5000;
  var COOKIE = "ah_j";
  var HLS_URL = "https://cdn.jsdelivr.net/npm/hls.js@1.5.20/dist/hls.min.js";
  var SUB_KEYS = ["sub1", "sub2", "sub3", "sub4", "sub5", "sub6", "sub7", "sub8", "sub9", "sub10",
    "utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"];

  // ---- pure helpers ------------------------------------------------------

  // "14:00", "1:02:03" or "840" in seconds; NaN when it is none of them.
  function parseTime(s) {
    if (s == null) return NaN;
    s = String(s).trim();
    if (s === "") return NaN;
    var parts = s.split(":");
    if (parts.length > 3) return NaN;
    var out = 0;
    for (var i = 0; i < parts.length; i++) {
      if (!/^\d+(\.\d+)?$/.test(parts[i])) return NaN;
      out = out * 60 + Number(parts[i]);
    }
    return out;
  }

  // FNV-1a, 32 bits: the same journey always gets the same arm.
  function hash32(s) {
    var h = 0x811c9dc5;
    for (var i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i);
      h = Math.imul(h, 0x01000193) >>> 0;
    }
    return h >>> 0;
  }

  function pickArm(key, arms) {
    if (!arms || !arms.length) return null;
    return arms[hash32(key) % arms.length];
  }

  // Adds second s to ranges: sorted, merged, half-open [from, to).
  function addSecond(ranges, s) {
    return mergeRanges(ranges.concat([[s, s + 1]]));
  }

  function mergeRanges(ranges) {
    var rs = ranges.filter(function (r) { return r[1] > r[0]; })
      .map(function (r) { return [r[0], r[1]]; })
      .sort(function (a, b) { return a[0] - b[0]; });
    var out = [];
    for (var i = 0; i < rs.length; i++) {
      var last = out[out.length - 1];
      if (last && rs[i][0] <= last[1]) last[1] = Math.max(last[1], rs[i][1]);
      else out.push(rs[i]);
    }
    return out;
  }

  function rangeSeconds(ranges) {
    var n = 0;
    for (var i = 0; i < ranges.length; i++) n += ranges[i][1] - ranges[i][0];
    return n;
  }

  // The share of the bar the smart progress bar shows at pos of len seconds:
  // fast at first, where most people leave, slower later. curve 1 is the
  // real time; the default 2 shows a quarter of the video as 44%.
  function smartProgress(pos, len, curve) {
    if (!(len > 0)) return 0;
    var x = Math.min(Math.max(pos / len, 0), 1);
    var k = curve > 0 ? curve : 2;
    return 1 - Math.pow(1 - x, k);
  }

  // The tracker click id and sub values in a query string.
  function readParams(search) {
    var q = new URLSearchParams(search || "");
    var subs = {};
    SUB_KEYS.forEach(function (k) {
      var v = q.get(k);
      if (v) subs[k] = v.slice(0, 200);
    });
    var c = q.get("clickid") || q.get("rtkclickid") || q.get("cid") || "";
    return { clickid: c.slice(0, 100), subs: subs };
  }

  function fillLink(href, clickid, journey) {
    return href.split("{clickid}").join(encodeURIComponent(clickid || ""))
      .split("{journey}").join(encodeURIComponent(journey || ""));
  }

  // The journey this page belongs to: the cookie's, unless the URL brings
  // another tracker click id, which starts a new one.
  function resolveJourney(saved, params, newId) {
    if (saved && saved.j && (!params.clickid || params.clickid === saved.c)) {
      var subs = saved.s || {};
      for (var k in params.subs) subs[k] = params.subs[k];
      return { j: saved.j, c: saved.c || "", s: subs, fresh: false };
    }
    return { j: newId(), c: params.clickid, s: params.subs, fresh: true };
  }

  function encodeCookie(o) {
    return encodeURIComponent(JSON.stringify({ j: o.j, c: o.c, s: o.s }));
  }

  function decodeCookie(cookie) {
    var m = ("; " + (cookie || "")).match(new RegExp("; " + COOKIE + "=([^;]*)"));
    if (!m) return null;
    try {
      var o = JSON.parse(decodeURIComponent(m[1]));
      return o && typeof o.j === "string" ? o : null;
    } catch (e) {
      return null;
    }
  }

  var api = {
    SCHEMA: SCHEMA, parseTime: parseTime, hash32: hash32, pickArm: pickArm,
    addSecond: addSecond, mergeRanges: mergeRanges, rangeSeconds: rangeSeconds,
    smartProgress: smartProgress, readParams: readParams, fillLink: fillLink,
    resolveJourney: resolveJourney, encodeCookie: encodeCookie, decodeCookie: decodeCookie,
  };
  root.AdHuntersFunnels = api;
  if (typeof document === "undefined" || root.__ahStarted) return;
  root.__ahStarted = true;

  // ---- the beacon --------------------------------------------------------

  var script = document.currentScript || document.querySelector('script[src*="ah.js"]');
  var ds = (script && script.dataset) || {};
  var endpoint = ds.endpoint || (script && script.src ? new URL("/e", script.src).href : "/e");
  var site = ds.site || location.hostname;
  var lpMeta = document.querySelector('meta[name="ah-lp"]');
  var lp = ds.lp || (lpMeta && lpMeta.content) || location.pathname;

  function newId() {
    var b = new Uint8Array(16);
    (root.crypto || root.msCrypto).getRandomValues(b);
    return Array.prototype.map.call(b, function (x) { return ("0" + x.toString(16)).slice(-2); }).join("");
  }

  var journey = resolveJourney(decodeCookie(document.cookie), readParams(location.search), newId);
  try {
    document.cookie = COOKIE + "=" + encodeCookie(journey) + "; path=/; max-age=86400; SameSite=Lax" +
      (location.protocol === "https:" ? "; Secure" : "");
  } catch (e) { /* cookies off: the journey lasts one page */ }

  var queue = [];
  var hadInput = false;
  var visibleMs = 0;
  var visibleSince = document.visibilityState === "visible" ? Date.now() : 0;
  var maxScroll = 0;
  var marks = {};
  var reached = {};
  var players = [];

  function push(ev) {
    ev.t = Date.now();
    queue.push(ev);
    if (queue.length >= 40) flush();
  }

  function takeVisible() {
    if (visibleSince) {
      var now = Date.now();
      visibleMs += now - visibleSince;
      visibleSince = now;
    }
    var out = visibleMs;
    visibleMs = 0;
    return out;
  }

  function flush() {
    if (!queue.length) return;
    var body = JSON.stringify({
      v: SCHEMA, j: journey.j, site: site, lp: lp, url: location.pathname + location.search,
      ref: document.referrer || "", c: journey.c, s: journey.s, in: hadInput ? 1 : 0,
      wd: navigator.webdriver ? 1 : 0, sw: screen.width, sh: screen.height, e: queue,
    });
    queue = [];
    var sent = false;
    try { sent = navigator.sendBeacon && navigator.sendBeacon(endpoint, body); } catch (e) { sent = false; }
    if (!sent) {
      try { fetch(endpoint, { method: "POST", body: body, keepalive: true, mode: "no-cors" }); } catch (e) { /* lost */ }
    }
  }

  function step(name) {
    if (!name || reached[name]) return;
    reached[name] = true;
    push({ k: "step", n: String(name).slice(0, 80) });
  }

  function scrollPct() {
    var h = document.documentElement.scrollHeight - innerHeight;
    return h <= 0 ? 100 : Math.min(100, Math.round((scrollY / h) * 100));
  }

  function onScroll() {
    var p = scrollPct();
    if (p > maxScroll) maxScroll = p;
    [25, 50, 75, 100].forEach(function (m) {
      if (maxScroll >= m && !marks[m]) {
        marks[m] = true;
        push({ k: "scroll", m: m });
      }
    });
  }

  function beat() {
    if (document.visibilityState !== "visible") return;
    push({ k: "beat", vis: takeVisible(), sc: maxScroll });
    players.forEach(function (p) { p.beat(); });
    flush();
  }

  function markInput() { hadInput = true; }

  function wire() {
    push({ k: "view", title: document.title.slice(0, 200) });

    ["pointerdown", "touchstart", "keydown", "wheel"].forEach(function (t) {
      addEventListener(t, markInput, { passive: true, once: true });
    });
    addEventListener("scroll", onScroll, { passive: true });
    onScroll();

    document.querySelectorAll("a[href]").forEach(function (a) {
      var h = a.getAttribute("href");
      if (h.indexOf("{clickid}") >= 0 || h.indexOf("{journey}") >= 0) a.setAttribute("href", fillLink(h, journey.c, journey.j));
    });

    document.addEventListener("click", function (e) {
      var el = e.target && e.target.closest && e.target.closest("[data-ah-click],[data-ah-step-on=click]");
      if (!el) return;
      if (el.dataset.ahClick) push({ k: "click", n: el.dataset.ahClick.slice(0, 80) });
      if (el.dataset.ahStepOn === "click") step(el.dataset.ahStep);
      flush();
    }, true);

    document.addEventListener("focusin", function (e) {
      var f = e.target && e.target.closest && e.target.closest("form");
      if (!f || f.__ahStarted) return;
      f.__ahStarted = true;
      push({ k: "form", n: (f.dataset.ahForm || f.id || "form").slice(0, 80) });
    });

    var steps = document.querySelectorAll("[data-ah-step]:not([data-ah-step-on=click])");
    if (steps.length && "IntersectionObserver" in root) {
      var io = new IntersectionObserver(function (entries) {
        entries.forEach(function (en) {
          if (en.isIntersecting) {
            step(en.target.dataset.ahStep);
            io.unobserve(en.target);
          }
        });
      }, { threshold: 0.5 });
      steps.forEach(function (el) { io.observe(el); });
    }

    document.querySelectorAll("[data-ah-video]").forEach(function (el) {
      try { players.push(mountPlayer(el)); } catch (e) { /* a broken player never breaks the page */ }
    });

    document.addEventListener("visibilitychange", function () {
      if (document.visibilityState === "visible") {
        visibleSince = Date.now();
      } else {
        push({ k: "beat", vis: takeVisible(), sc: maxScroll });
        visibleSince = 0;
        players.forEach(function (p) { p.beat(); });
        flush();
      }
    });
    addEventListener("pagehide", function () {
      push({ k: "exit", vis: takeVisible(), sc: maxScroll });
      players.forEach(function (p) { p.beat(); });
      flush();
    });
    setInterval(beat, BEAT_MS);
    flush();
  }

  // ---- Microsoft Clarity -------------------------------------------------

  // Recordings and heatmaps. Tagged with the journey and tracker click id,
  // so a recording can be found from a funnel number and the other way.
  function clarity(id) {
    if (!/^[a-z0-9]{6,20}$/i.test(id)) return;
    root.clarity = root.clarity || function () { (root.clarity.q = root.clarity.q || []).push(arguments); };
    var s = document.createElement("script");
    s.async = true;
    s.src = "https://www.clarity.ms/tag/" + id;
    document.head.appendChild(s);
    root.clarity("set", "journey", journey.j);
    if (journey.c) root.clarity("set", "clickid", journey.c);
    ["sub1", "sub4", "sub8"].forEach(function (k) { if (journey.s[k]) root.clarity("set", k, journey.s[k]); });
    root.clarity("set", "lp", String(lp));
  }

  // ---- the VSL player ----------------------------------------------------

  var cssDone = false;
  function css() {
    if (cssDone) return;
    cssDone = true;
    var st = document.createElement("style");
    st.textContent =
      ".ahp{position:relative;background:#000;aspect-ratio:16/9;overflow:hidden;cursor:pointer}" +
      ".ahp video{width:100%;height:100%;display:block;object-fit:contain}" +
      ".ahp-bar{position:absolute;left:0;right:0;bottom:0;height:6px;background:rgba(255,255,255,.25)}" +
      ".ahp-bar i{display:block;height:100%;width:0;background:#e3342f;transition:width .25s linear}" +
      ".ahp-box{position:absolute;inset:0;display:flex;align-items:center;justify-content:center}" +
      ".ahp-box div{background:#e3342f;color:#fff;font:600 18px/1.35 system-ui,sans-serif;text-align:center;" +
      "padding:18px 22px;border-radius:10px;max-width:80%;box-shadow:0 6px 24px rgba(0,0,0,.4)}" +
      ".ahp-box b{display:block;font-size:22px;margin-top:6px}" +
      ".ahp-box button{font:600 16px system-ui,sans-serif;margin:10px 6px 0;padding:10px 16px;border:0;border-radius:6px;cursor:pointer}" +
      "[data-show-at]:not(.ahp-shown){display:none!important}";
    document.head.appendChild(st);
  }

  function loadHls(cb) {
    if (root.Hls) return cb(root.Hls);
    var s = document.createElement("script");
    s.src = HLS_URL;
    s.onload = function () { cb(root.Hls); };
    s.onerror = function () { cb(null); };
    document.head.appendChild(s);
  }

  function store(key, val) {
    try {
      if (val === undefined) return localStorage.getItem(key);
      localStorage.setItem(key, String(val));
    } catch (e) { /* storage off */ }
    return null;
  }

  // <div data-ah-video="bp-02" data-src="https://…/playlist.m3u8"
  //      data-poster="…" data-pitch="14:00" data-curve="2"
  //      data-arms='[{"id":"a","src":"…","pitch":"13:40"},{"id":"b","src":"…"}]'
  //      data-unmute-text="Your video has already started" data-unmute-cta="Click to listen"></div>
  function mountPlayer(el) {
    css();
    var d = el.dataset;
    var vid = d.ahVideo.slice(0, 80);
    var arms = null;
    try { arms = d.arms ? JSON.parse(d.arms) : null; } catch (e) { arms = null; }
    var arm = pickArm(journey.j + ":" + vid, arms);
    var src = (arm && arm.src) || d.src;
    var pitch = parseTime((arm && arm.pitch) || d.pitch);
    var poster = (arm && arm.poster) || d.poster;
    var curve = Number(d.curve) || 2;
    var armId = arm ? String(arm.id || "") : "";
    var posKey = "ah_pos_" + vid;
    var shownKey = "ah_shown_" + vid;

    el.classList.add("ahp");
    var video = document.createElement("video");
    video.muted = true;
    video.playsInline = true;
    video.setAttribute("playsinline", "");
    video.setAttribute("webkit-playsinline", "");
    video.preload = "metadata";
    if (poster) video.poster = poster;
    var bar = document.createElement("div");
    bar.className = "ahp-bar";
    var fill = document.createElement("i");
    bar.appendChild(fill);
    var box = document.createElement("div");
    box.className = "ahp-box";
    el.appendChild(video);
    el.appendChild(bar);
    el.appendChild(box);

    var reveal = Array.prototype.filter.call(document.querySelectorAll("[data-show-at]"), function (x) {
      var owner = x.dataset.showFor;
      return !owner || owner === vid;
    });
    var shownUntil = Number(store(shownKey)) || 0;
    function showUpTo(sec) {
      reveal.forEach(function (x) {
        if (!x.classList.contains("ahp-shown") && parseTime(x.dataset.showAt) <= sec) {
          x.classList.add("ahp-shown");
          if (sec > shownUntil) {
            shownUntil = sec;
            store(shownKey, sec);
          }
        }
      });
    }
    showUpTo(shownUntil);

    var listening = false; // unmuted, or started with sound: the seconds count
    var pending = [];      // watched ranges not sent yet
    var total = [];        // every watched range on this page
    var pitchSent = false;
    var lastSec = -1;
    var restarting = false; // our own jump to the start, not a seek

    function ev(name, extra) {
      var e = { k: "video", id: vid, ev: name, pos: Math.floor(video.currentTime || 0) };
      if (video.duration && isFinite(video.duration)) e.len = Math.round(video.duration);
      if (pitch >= 0) e.pitch = pitch;
      if (armId) e.a = armId;
      if (extra) for (var k in extra) e[k] = extra[k];
      push(e);
    }

    function message(html, buttons) {
      box.innerHTML = "";
      if (!html) return;
      var m = document.createElement("div");
      m.innerHTML = html;
      (buttons || []).forEach(function (b) {
        var btn = document.createElement("button");
        btn.textContent = b[0];
        btn.addEventListener("click", function (e) { e.stopPropagation(); b[1](); });
        m.appendChild(btn);
      });
      box.appendChild(m);
    }

    function esc(s) {
      return String(s).replace(/[&<>"]/g, function (c) { return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]; });
    }

    function listen(from) {
      listening = true;
      video.muted = false;
      restarting = true;
      video.currentTime = from;
      message("");
      var p = video.play();
      if (p && p.catch) p.catch(function () {});
    }

    function start() {
      var saved = Number(store(posKey)) || 0;
      var len = video.duration || 0;
      if (saved > 30 && (!len || saved < len - 10)) {
        video.pause();
        message(esc("You already started watching this video."), [
          ["Continue watching", function () { ev("resume", { from: saved }); ev("play"); listen(saved); }],
          ["Start over", function () { ev("play"); listen(0); }],
        ]);
        return;
      }
      ev("play");
      listen(0);
    }

    video.addEventListener("timeupdate", function () {
      var pos = video.currentTime || 0;
      var len = video.duration || 0;
      fill.style.width = (smartProgress(pos, len, curve) * 100).toFixed(2) + "%";
      if (!listening || video.paused) return;
      var s = Math.floor(pos);
      if (s !== lastSec) {
        lastSec = s;
        pending = addSecond(pending, s);
        total = addSecond(total, s);
        if (s % 5 === 0) store(posKey, s);
        showUpTo(s);
        if (pitch >= 0 && s >= pitch && !pitchSent) {
          pitchSent = true;
          ev("pitch");
          flush();
        }
      }
    });
    video.addEventListener("ended", function () { if (listening) { ev("end"); store(posKey, 0); flush(); } });
    video.addEventListener("pause", function () { if (listening && !video.ended) ev("pause"); });
    video.addEventListener("seeked", function () {
      if (restarting) restarting = false;
      else if (listening) ev("seek");
    });
    video.addEventListener("loadedmetadata", function () { ev("load"); }, { once: true });

    el.addEventListener("click", function () {
      if (!listening) return start();
      if (video.paused) {
        var p = video.play();
        if (p && p.catch) p.catch(function () {});
        ev("resume", { from: Math.floor(video.currentTime || 0) });
      } else {
        video.pause();
      }
    });

    function autoplay() {
      var p = video.play();
      var ok = function () {
        ev("autoplay");
        message(esc(d.unmuteText || "Your video has already started") + "<b>" + esc(d.unmuteCta || "Click to listen") + "</b>");
      };
      var blocked = function () {
        message("<b>" + esc(d.playCta || "Click to watch") + "</b>");
      };
      if (p && p.then) p.then(ok, blocked); else ok();
    }

    function attach() {
      if (/\.m3u8(\?|$)/.test(src) && !video.canPlayType("application/vnd.apple.mpegurl")) {
        loadHls(function (Hls) {
          if (Hls && Hls.isSupported()) {
            var h = new Hls({ capLevelToPlayerSize: true });
            h.loadSource(src);
            h.attachMedia(video);
          } else {
            video.src = src;
          }
          autoplay();
        });
      } else {
        video.src = src;
        autoplay();
      }
    }
    attach();

    return {
      beat: function () {
        if (!pending.length) return;
        ev("beat", { w: pending, ws: rangeSeconds(total) });
        pending = [];
      },
    };
  }

  function boot() {
    if (ds.clarity) {
      try { clarity(ds.clarity); } catch (e) { /* recordings are optional */ }
    }
    wire();
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})(typeof window !== "undefined" ? window : globalThis);

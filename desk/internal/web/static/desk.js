// Desk's pages in the Frame, and a conversation that keeps itself up to
// date: it asks every few seconds whether anything changed and, when it
// did, redraws the conversation (never the message being written, nor the
// boxes already ticked).
import { mountFrame } from '/desk/_frame/frame.js';

const body = document.body;
mountFrame({
  app: 'desk',
  tabs: [
    { id: 'talk', label: 'Conversas', href: '/desk/' },
    { id: 'todos', label: 'A fazer', href: '/desk/todos' },
    { id: 'settings', label: 'Ajustes', href: '/desk/settings' },
  ],
  active: body.dataset.tab,
  user: body.dataset.user,
  ready: { desk: true },
  searchLabel: 'Buscar apps',
});

const timeline = document.getElementById('timeline');
if (timeline) {
  const conv = timeline.dataset.conv;
  let v = timeline.dataset.v;
  let busy = false;
  const every = 3000;

  async function look() {
    if (busy || document.visibilityState !== 'visible') return;
    busy = true;
    try {
      const r = await fetch(`/desk/c/${conv}/v`, { headers: { Accept: 'application/json' }, cache: 'no-store' });
      if (!r.ok) return;
      const now = (await r.json()).v;
      if (now === v) return;
      const page = await fetch(`/desk/c/${conv}?part=timeline`, { cache: 'no-store' });
      if (!page.ok) return;
      const html = await page.text();
      // Keep what the person ticked in a choice still open.
      const ticked = new Map();
      for (const box of timeline.querySelectorAll('input[data-keep]')) {
        ticked.set(box.form?.action + '|' + box.value, box.checked);
      }
      const atEnd = window.innerHeight + window.scrollY >= document.body.scrollHeight - 80;
      // The HTML is Desk's own page, drawn and escaped by the server.
      timeline.innerHTML = html;
      for (const box of timeline.querySelectorAll('input[data-keep]')) {
        const key = box.form?.action + '|' + box.value;
        if (ticked.has(key)) box.checked = ticked.get(key);
      }
      v = now;
      if (atEnd) document.getElementById('end')?.scrollIntoView({ block: 'end' });
    } catch {
      // The next look tries again.
    } finally {
      busy = false;
    }
  }
  setInterval(look, every);
  document.addEventListener('visibilitychange', look);
  if (location.hash === '' || location.hash === '#end') document.getElementById('end')?.scrollIntoView({ block: 'end' });
}

// Ticking an option marks its picture.
document.addEventListener('change', (e) => {
  const box = e.target;
  if (!(box instanceof HTMLInputElement) || box.name !== 'pick') return;
  box.closest('.thumb')?.classList.toggle('chosen', box.checked);
});

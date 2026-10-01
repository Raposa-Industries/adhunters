// Funnels' pages in the Frame. The server draws the page; this mounts the
// Frame around it and wires ⌘K to sites, landing pages, videos and click
// ids.
import { mountFrame } from '../_frame/frame.js';

const body = document.body;
const tabs = [
  { id: 'home', label: 'Visão geral', href: '/funnels/' },
  { id: 'videos', label: 'Vídeos', href: '/funnels/videos' },
  { id: 'journeys', label: 'Jornadas', href: '/funnels/journeys' },
];
if (body.dataset.hosting) tabs.push({ id: 'hosting', label: 'Hospedagem', href: '/funnels/hosting' });

mountFrame({
  app: 'funnels',
  tabs,
  active: body.dataset.tab,
  user: body.dataset.user || undefined,
  ready: { funnels: true },
  searchLabel: 'Buscar sites, páginas, vídeos, click id',
  search: async (q) => {
    const r = await fetch('/funnels/api/search?q=' + encodeURIComponent(q));
    return r.ok ? r.json() : [];
  },
});

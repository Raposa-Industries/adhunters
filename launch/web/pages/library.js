// The team's library in Novo par: the sets Create saved, each with its
// creatives and headlines, to use in a pair. Launch only reads it.
import { api, h, note, select, plural, date, busy } from './lib.js';

// libraryPicker is the "Da biblioteca" box. use(creatives, headlines) adds
// the chosen ones to the pair; open(id) shows one set.
export function libraryPicker(use) {
  const out = h('div');
  const body = h('div', { class: 'lib-body' });
  const vert = select([['', 'Todas as verticais']], '', { 'aria-label': 'Vertical' });
  const box = h('details', { class: 'library' },
    h('summary', {}, 'Da biblioteca'),
    h('p', { class: 'faint' }, 'Criativos e headlines salvos no Create, por conjunto.'),
    h('div', { class: 'actions' }, vert), out, body);
  let verticals = new Map();
  let loaded = false;
  let current = null; // the set shown, or null for the list

  async function load() {
    if (loaded) return;
    loaded = true;
    try {
      const v = (await api('library/verticals')).verticals || [];
      verticals = new Map(v.map((x) => [x.id, x.name]));
      vert.replaceChildren(h('option', { value: '' }, 'Todas as verticais'), ...v.map((x) => h('option', { value: x.id }, x.name)));
    } catch (e) {
      out.replaceChildren(note('warn', e.message));
      loaded = false;
      return;
    }
    if (current === null) await sets();
  }

  async function sets() {
    current = null;
    body.replaceChildren(h('p', { class: 'faint' }, 'Carregando…'));
    try {
      const list = (await api('library/sets?limit=30' + (vert.value ? '&vertical=' + encodeURIComponent(vert.value) : ''))).sets || [];
      body.replaceChildren(list.length ? h('ul', { class: 'lib-sets' }, list.map((x) => h('li', {},
        h('button', { type: 'button', class: 'link', onclick: () => open(x.id) }, x.name),
        h('span', { class: 'faint' }, ' · ', verticals.get(x.vertical_id) || x.vertical_id || 'sem vertical', ' · ',
          plural(x.creatives, 'criativo', 'criativos'), ' · ', plural(x.headlines, 'headline', 'headlines'), ' · ', date(x.created_at))))) :
        h('p', { class: 'empty' }, 'Nada salvo ainda' + (vert.value ? ' nesta vertical.' : '.')));
    } catch (e) {
      body.replaceChildren(note('warn', e.message));
    }
  }

  // open shows one set, everything checked, with a button to use it.
  async function open(id, now = false) {
    current = id;
    box.open = true;
    body.replaceChildren(h('p', { class: 'faint' }, 'Carregando…'));
    let d;
    try {
      d = await api('library/set?id=' + encodeURIComponent(id));
    } catch (e) {
      body.replaceChildren(note('fail', e.message));
      return;
    }
    const cs = (d.creatives || []).filter((c) => !c.hidden).map((c) => ({ c, cb: h('input', { type: 'checkbox', checked: true }) }));
    const hs = (d.headlines || []).filter((x) => !x.hidden).map((x) => ({ x, cb: h('input', { type: 'checkbox', checked: true }) }));
    const msg = h('div');
    const go = h('button', { type: 'button', class: 'primary', onclick: () => busy(go, msg, async () => {
      const chosen = cs.filter((x) => x.cb.checked).map((x) => x.c);
      const lines = hs.filter((x) => x.cb.checked).map((x) => x.x);
      if (!chosen.length && !lines.length) throw new Error('Marque ao menos um criativo ou headline.');
      const got = await use(chosen, lines);
      msg.replaceChildren(note(got.problems ? 'warn' : 'ok', `${plural(got.images, 'imagem nova', 'imagens novas')} e ${plural(got.headlines, 'headline nova', 'headlines novas')} no par.`,
        got.problems ? ' Veja abaixo o que não entrou.' : ''));
    }) }, 'Usar na campanha');
    body.replaceChildren(
      h('p', {}, h('button', { type: 'button', class: 'link', onclick: () => sets() }, '← Conjuntos'), ' · ', h('b', {}, d.set?.name || 'Conjunto ' + id)),
      cs.length ? h('div', { class: 'thumbs' }, cs.map(({ c, cb }) => h('figure', { class: 'thumb' },
        h('img', { src: '/launch/api/library/thumb?id=' + c.id, alt: '', loading: 'lazy' }),
        h('figcaption', {}, h('label', { class: 'check' }, cb, c.name || 'criativo ' + c.id),
          h('div', { class: 'faint' }, [c.width && `${c.width}×${c.height}`, c.angle, c.ai_label === 'ai' ? 'feito com IA' : c.ai_label === 'not_ai' ? 'sem IA' : ''].filter(Boolean).join(' · ')))))) :
        h('p', { class: 'faint' }, 'Sem criativos neste conjunto.'),
      hs.length ? h('ul', { class: 'lib-headlines' }, hs.map(({ x, cb }) => h('li', {}, h('label', { class: 'check' }, cb, x.text)))) : h('p', { class: 'faint' }, 'Sem headlines neste conjunto.'),
      h('div', { class: 'actions' }, go), msg);
    if (now) go.click();
  }

  box.addEventListener('toggle', () => { if (box.open) load(); });
  vert.addEventListener('change', () => sets());
  return { el: box, open };
}

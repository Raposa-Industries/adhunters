#!/usr/bin/env python3
"""Diff two Figma Bridge whole-file exports (exports/<fileKey>/*.json).

usage: diff.py OLD.json NEW.json OUTDIR [PNGDIR]
Writes OUTDIR/diff.json, OUTDIR/diff.md, OUTDIR/frames/<slug>.json (full tree) and
OUTDIR/frames/<slug>.outline.txt for every new or changed top-level frame, and copies
their PNGs from PNGDIR (files named <nodeId with : ; as ->.png) into OUTDIR/png/.
"""
import json, os, re, shutil, sys

GEOM = ('x', 'y', 'w', 'h')
LAYOUT = ('layout', 'sizing', 'visible')
STYLE = ('fills', 'stroke', 'radius', 'effects', 'opacity', 'bind', 'font', 'size', 'color', 'align')
COPY = ('text',)
OTHER = ('name', 'type', 'of', 'instanceProps', 'notes', 'description', 'svg', 'key')


def slug(s):
    s = re.sub(r'[^\w]+', '-', s, flags=re.UNICODE).strip('-')
    return s[:80] or 'frame'


def flatten(n, out, path=''):
    p = (path + ' / ' if path else '') + n.get('name', '')
    out[n['id']] = (n, p)
    for c in n.get('children', []) or []:
        flatten(c, out, p)
    return out


def props(n):
    return {k: v for k, v in n.items() if k != 'children'}


def node_diff(a, b):
    """field -> (old, new) for fields that differ (children ignored)."""
    pa, pb = props(a), props(b)
    d = {}
    for k in set(pa) | set(pb):
        if k == 'id':
            continue
        if pa.get(k) != pb.get(k):
            d[k] = (pa.get(k), pb.get(k))
    return d


def classify(fields):
    cats = set()
    for k in fields:
        if k in COPY: cats.add('copy')
        elif k in GEOM or k in LAYOUT: cats.add('layout')
        elif k in STYLE: cats.add('tokens/style')
        else: cats.add('other')
    return cats


def outline(n, depth=0, lines=None):
    lines = [] if lines is None else lines
    bits = [n.get('type', '?'), repr(n.get('name', ''))]
    if 'key' in n: bits.append('key=' + n['key'])
    if 'w' in n: bits.append('%sx%s @%s,%s' % (n.get('w'), n.get('h'), n.get('x'), n.get('y')))
    lay = n.get('layout')
    if lay: bits.append('layout=%s gap=%s pad=%s main=%s cross=%s' % (lay.get('mode'), lay.get('gap'), lay.get('padding'), lay.get('main'), lay.get('cross')))
    if n.get('sizing'): bits.append('sizing=%s/%s' % (n['sizing'].get('h'), n['sizing'].get('v')))
    if n.get('fills'): bits.append('fills=' + json.dumps(n['fills'])[:80])
    if n.get('stroke'): bits.append('stroke=' + json.dumps(n['stroke'])[:80])
    if n.get('radius'): bits.append('radius=%s' % (n['radius'],))
    if n.get('bind'): bits.append('bind=' + json.dumps(n['bind']))
    if n.get('type') == 'TEXT':
        f = n.get('font') or {}
        bits.append('font=%s %s %s color=%s' % (f.get('family'), f.get('style'), n.get('size'), n.get('color')))
        bits.append('text=' + json.dumps(n.get('text', ''), ensure_ascii=False))
    if n.get('of'): bits.append('instanceOf=%s' % (n['of'].get('key') or n['of'].get('name')))
    if n.get('instanceProps'): bits.append('props=' + json.dumps(n['instanceProps'], ensure_ascii=False))
    if n.get('visible') is False: bits.append('HIDDEN')
    if n.get('notes'): bits.append('notes=' + json.dumps(n['notes'], ensure_ascii=False))
    lines.append('  ' * depth + ' '.join(str(b) for b in bits))
    for c in n.get('children', []) or []:
        outline(c, depth + 1, lines)
    return lines


def short(v, n=90):
    s = json.dumps(v, ensure_ascii=False)
    return s if len(s) <= n else s[:n] + '…'


def main():
    old_p, new_p, outdir = sys.argv[1:4]
    pngdir = sys.argv[4] if len(sys.argv) > 4 else None
    old, new = json.load(open(old_p)), json.load(open(new_p))
    os.makedirs(os.path.join(outdir, 'frames'), exist_ok=True)

    def tops(d):
        t = {}
        for p in d['pages']:
            for f in p.get('children', []) or []:
                t[f['id']] = (p['name'], f)
        return t

    ot, nt = tops(old), tops(new)
    oldpages = [p['name'] for p in old['pages']]
    newpages = [p['name'] for p in new['pages']]
    res = {'old': {'exportedAt': old.get('exportedAt'), 'file': old_p}, 'new': {'exportedAt': new.get('exportedAt'), 'file': new_p},
           'pages': {'added': [p for p in newpages if p not in oldpages], 'removed': [p for p in oldpages if p not in newpages]},
           'frames': {'added': [], 'removed': [], 'changed': [], 'unchanged': 0}}

    for fid, (pg, f) in ot.items():
        if fid not in nt:
            res['frames']['removed'].append({'id': fid, 'page': pg, 'name': f['name'], 'key': f.get('key'), 'type': f['type']})

    for fid, (pg, f) in nt.items():
        entry = {'id': fid, 'page': pg, 'name': f['name'], 'key': f.get('key'), 'type': f['type'], 'w': f.get('w'), 'h': f.get('h')}
        if fid not in ot:
            nn = flatten(f, {})
            entry['nodes'] = len(nn)
            entry['texts'] = [n.get('text') for n, _ in nn.values() if n.get('type') == 'TEXT'][:200]
            res['frames']['added'].append(entry)
            continue
        opg, of = ot[fid]
        a, b = flatten(of, {}), flatten(f, {})
        added = [i for i in b if i not in a]
        removed = [i for i in a if i not in b]
        changes = []
        for i in b:
            if i in a:
                d = node_diff(a[i][0], b[i][0])
                if i == fid:  # top-level position on the page is not a design change
                    d.pop('x', None); d.pop('y', None)
                if d:
                    changes.append({'id': i, 'path': b[i][1], 'type': b[i][0].get('type'), 'key': b[i][0].get('key'),
                                    'cats': sorted(classify(d)), 'fields': {k: {'old': v[0], 'new': v[1]} for k, v in d.items()}})
        if pg != opg:
            entry['movedFromPage'] = opg
        if not (added or removed or changes or pg != opg):
            res['frames']['unchanged'] += 1
            continue
        cats = set()
        for c in changes: cats |= set(c['cats'])
        if added or removed: cats.add('structure')
        entry.update({'categories': sorted(cats),
                      'nodesAdded': [{'id': i, 'path': b[i][1], 'type': b[i][0].get('type'), 'key': b[i][0].get('key'), 'text': b[i][0].get('text')} for i in added],
                      'nodesRemoved': [{'id': i, 'path': a[i][1], 'type': a[i][0].get('type'), 'key': a[i][0].get('key'), 'text': a[i][0].get('text')} for i in removed],
                      'nodeChanges': changes})
        res['frames']['changed'].append(entry)

    # variables
    def vmap(d):
        m = {}
        for c in d.get('variables') or []:
            for v in c.get('vars', []):
                m[c['collection'] + '/' + v['name']] = v
        return m
    ov, nv = vmap(old), vmap(new)
    res['variables'] = {'added': [dict(name=k, **nv[k]) for k in nv if k not in ov],
                        'removed': [k for k in ov if k not in nv],
                        'changed': [{'name': k, 'old': ov[k].get('value'), 'new': nv[k].get('value')} for k in nv if k in ov and ov[k] != nv[k]],
                        'total': len(nv), 'collections': [{'collection': c['collection'], 'modes': c.get('modes'), 'count': len(c.get('vars', []))} for c in new.get('variables') or []]}

    # per-frame payloads
    pngs = new.get('pngs') or {}
    for e in res['frames']['added'] + res['frames']['changed']:
        s = slug('%s-%s' % (e['page'], e['name']))
        tree = nt[e['id']][1]
        json.dump(dict(tree, page=e['page']), open(os.path.join(outdir, 'frames', s + '.json'), 'w'), ensure_ascii=False, indent=1)
        open(os.path.join(outdir, 'frames', s + '.outline.txt'), 'w').write('\n'.join(outline(tree)) + '\n')
        e['tree'] = 'frames/%s.json' % s
        e['outline'] = 'frames/%s.outline.txt' % s
        fn = e['id'].replace(':', '-').replace(';', '-') + '.png'
        src = os.path.join(pngdir, fn) if pngdir else None
        if src and os.path.exists(src):
            os.makedirs(os.path.join(outdir, 'png'), exist_ok=True)
            shutil.copy(src, os.path.join(outdir, 'png', s + '.png'))
            e['png'] = 'png/%s.png' % s

    json.dump(res, open(os.path.join(outdir, 'diff.json'), 'w'), ensure_ascii=False, indent=1)

    # markdown summary
    L = ['# AdHunters · Ember: diff of export %s against %s' % (res['new']['exportedAt'], res['old']['exportedAt']), '']
    L.append('Pages added: %s. Pages removed: %s.' % (res['pages']['added'] or 'none', res['pages']['removed'] or 'none'))
    L.append('Top-level frames: %d added, %d removed, %d changed, %d unchanged.' % (
        len(res['frames']['added']), len(res['frames']['removed']), len(res['frames']['changed']), res['frames']['unchanged']))
    L.append('')
    if res['frames']['added']:
        L.append('## Added')
        for e in res['frames']['added']:
            L.append('- **%s / %s** (%s, id %s, %sx%s, %d layers) tree `%s`, outline `%s`%s' % (
                e['page'], e['name'], e['type'], e['id'], e['w'], e['h'], e['nodes'], e['tree'], e['outline'], ', png `%s`' % e['png'] if e.get('png') else ''))
        L.append('')
    if res['frames']['removed']:
        L.append('## Removed')
        for e in res['frames']['removed']:
            L.append('- %s / %s (%s, id %s, key %s)' % (e['page'], e['name'], e['type'], e['id'], e['key']))
        L.append('')
    if res['frames']['changed']:
        L.append('## Changed')
        for e in res['frames']['changed']:
            L.append('### %s / %s (id %s)%s' % (e['page'], e['name'], e['id'], ' moved from page ' + e['movedFromPage'] if e.get('movedFromPage') else ''))
            L.append('Kinds: %s. %d layers added, %d removed, %d changed. Tree `%s`%s.' % (
                ', '.join(e['categories']), len(e['nodesAdded']), len(e['nodesRemoved']), len(e['nodeChanges']), e['tree'], ', png `%s`' % e['png'] if e.get('png') else ''))
            for x in e['nodesAdded'][:25]:
                L.append('- added %s `%s`%s' % (x['type'], x['path'], ' text ' + short(x['text']) if x.get('text') else ''))
            if len(e['nodesAdded']) > 25: L.append('- … %d more added (see diff.json)' % (len(e['nodesAdded']) - 25))
            for x in e['nodesRemoved'][:25]:
                L.append('- removed %s `%s`%s' % (x['type'], x['path'], ' text ' + short(x['text']) if x.get('text') else ''))
            if len(e['nodesRemoved']) > 25: L.append('- … %d more removed (see diff.json)' % (len(e['nodesRemoved']) - 25))
            for c in e['nodeChanges'][:40]:
                fs = '; '.join('%s: %s → %s' % (k, short(v['old'], 60), short(v['new'], 60)) for k, v in c['fields'].items() if k != 'svg')
                if 'svg' in c['fields']: fs += '; svg changed'
                L.append('- changed `%s`: %s' % (c['path'], fs))
            if len(e['nodeChanges']) > 40: L.append('- … %d more changed (see diff.json)' % (len(e['nodeChanges']) - 40))
            L.append('')
    V = res['variables']
    L.append('## Variables')
    L.append('Now %d variables in %s. Added %d, removed %d, changed %d vs the old export.' % (
        V['total'], ', '.join('%s (%d, modes %s)' % (c['collection'], c['count'], c['modes']) for c in V['collections']) or 'no collections',
        len(V['added']), len(V['removed']), len(V['changed'])))
    for v in V['added'][:60]: L.append('- added %s = %s' % (v['name'], short(v.get('value'))))
    for v in V['removed'][:60]: L.append('- removed %s' % v)
    for v in V['changed'][:60]: L.append('- changed %s: %s → %s' % (v['name'], short(v['old']), short(v['new'])))
    open(os.path.join(outdir, 'diff.md'), 'w').write('\n'.join(L) + '\n')
    print('\n'.join(L[:6]))


if __name__ == '__main__':
    main()

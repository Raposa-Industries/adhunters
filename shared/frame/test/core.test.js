// The Frame's rules: node --test shared/frame/test/*.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { APPS, apps, tail, switchTo, Chord, typing, fold, score, rank, marks, initials } from '../assets/core.js';

test('apps are in the order the team works, each with its own key', () => {
  assert.deepEqual(APPS.map((a) => a.id), ['spy', 'create', 'launch', 'intel', 'funnels', 'raposa', 'desk']);
  assert.equal(new Set(APPS.map((a) => a.key)).size, APPS.length);
  assert.equal(apps({ raposa: true }).find((a) => a.id === 'raposa').ready, true);
  assert.equal(APPS.find((a) => a.id === 'raposa').ready, false, 'the list itself is not changed');
});

test('tail is what follows the app in the path', () => {
  assert.equal(tail('launch', '/launch/taboola/acme-sc/g/1/c/2'), '/taboola/acme-sc/g/1/c/2');
  assert.equal(tail('launch', '/launch/'), '');
  assert.equal(tail('launch', '/launch'), '');
  assert.equal(tail('launch', '/launchpad/x'), '');
  assert.equal(tail('launch', '/create/x'), '');
});

test('switching keeps the Taboola object between Launch and Intel only', () => {
  const at = '/launch/taboola/zoltagroup-2-sc/g/1157401/c/50561234';
  assert.equal(switchTo('launch', 'intel', at), '/intel/taboola/zoltagroup-2-sc/g/1157401/c/50561234');
  assert.equal(switchTo('intel', 'launch', '/intel/taboola/a-sc'), '/launch/taboola/a-sc');
  assert.equal(switchTo('launch', 'create', at), '/create/');
  assert.equal(switchTo('launch', 'intel', '/launch/presets'), '/intel/', 'a page that is not the tree goes home');
  assert.equal(switchTo('create', 'launch', '/create/taboola/x'), '/launch/');
});

test('G then a letter names an app, within the window', () => {
  const c = new Chord(APPS, 1500);
  assert.equal(c.key('g', 0), null);
  assert.equal(c.key('l', 400).id, 'launch');
  assert.equal(c.key('i', 500), null, 'one chord, one app');
  c.key('G', 1000);
  assert.equal(c.key('i', 3000), null, 'too late');
  c.key('g', 4000);
  assert.equal(c.key('x', 4100), null, 'not an app');
  assert.equal(c.key('i', 4200), null, 'x disarmed it');
  c.key('g', 5000);
  assert.equal(c.key('g', 5100), null, 'G twice re-arms');
  assert.equal(c.key('c', 5200).id, 'create');
});

test('a chord only reaches the apps it was given', () => {
  const c = new Chord(apps().filter((a) => a.ready));
  c.key('g', 0);
  assert.equal(c.key('r', 10), null, 'Raposa is not up yet');
});

test('letters typed in a field are text, not shortcuts', () => {
  assert.equal(typing({ tagName: 'INPUT', type: 'text' }), true);
  assert.equal(typing({ tagName: 'INPUT', type: 'search' }), true);
  assert.equal(typing({ tagName: 'INPUT', type: 'checkbox' }), false);
  assert.equal(typing({ tagName: 'TEXTAREA' }), true);
  assert.equal(typing({ tagName: 'SELECT' }), true);
  assert.equal(typing({ tagName: 'DIV', isContentEditable: true }), true);
  assert.equal(typing({ tagName: 'BUTTON' }), false);
  assert.equal(typing(null), false);
});

test('search folds accents and ranks starts first', () => {
  assert.equal(fold('Memória Pressão'), 'memoria pressao');
  assert.equal(score('Blood Pressure', 'press'), 2);
  assert.equal(score('Blood Pressure', 'blood'), 3);
  assert.equal(score('Blood Pressure', 'lood'), 1);
  assert.equal(score('Blood Pressure', 'blood x'), 0, 'every word must match');
  const got = rank([{ title: 'Unblood' }, { title: 'Pressure, blood' }, { title: 'Blood Pressure' }, { title: 'Tinnitus' }], 'blood');
  assert.deepEqual(got.map((e) => e.title), ['Blood Pressure', 'Pressure, blood', 'Unblood']);
  assert.deepEqual(rank([{ title: 'Launch', words: 'campanhas' }], 'campa').map((e) => e.title), ['Launch']);
});

test('marks show what matched, accents kept', () => {
  assert.deepEqual(marks('Memória Loss', 'memoria'), [{ text: 'Memória', hit: true }, { text: ' Loss', hit: false }]);
  assert.deepEqual(marks('abc', ''), [{ text: 'abc', hit: false }]);
  assert.deepEqual(marks('', 'x'), []);
});

test('initials come from a name or an email', () => {
  assert.equal(initials('Marcos Capistrano'), 'MC');
  assert.equal(initials('mari@example.com'), 'MA');
  assert.equal(initials('vini.souza@example.com'), 'VS');
  assert.equal(initials(''), '?');
});

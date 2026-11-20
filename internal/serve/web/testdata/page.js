'use strict';
// node page.js <static dir>: runs live.js and app.js on a stub DOM, fetch and EventSource; prints one line per failed check.
const fs = require('node:fs'), path = require('node:path'), vm = require('node:vm');

function node() {
  const n = { children: [], dataset: {}, style: { setProperty() {} }, hidden: false, inert: false, scrollTop: 0, className: '', textContent: '',
    classList: { set: new Set(), toggle(c, on) { on ? this.set.add(c) : this.set.delete(c); }, contains(c) { return this.set.has(c); } },
    setAttribute() {}, getAttribute() { return null; }, addEventListener() {}, focus() {},
    append(...k) { n.children.push(...k); }, replaceChildren(...k) { n.children = k; },
    querySelectorAll() { return []; }, querySelector() { return null; } };
  return n;
}
const els = {};
let version = 'v1', tag = 'A', status = 200;
const board = () => ({ version, statuses: [], counts: { decide: 0, yours: 0 }, manager_runs: false, generated: 1,
  lists: [{ boss: 'boss-a', items: [{ id: 1, text: 'item ' + tag, done: false, status: 'yours' }] }] });
const calls = [];
const ctx = {
  console, Date, JSON, Math, Number, String, Object, Array, Set, Map, Promise, RegExp, Error, AbortController,
  setTimeout, clearTimeout, setInterval: () => 0,
  document: { visibilityState: 'visible', activeElement: null, documentElement: node(), getElementById: id => els[id] ||= node(),
    createElement: () => node(), addEventListener() {}, querySelectorAll: () => [] },
  window: { addEventListener() {}, scrollTo() {} },
  location: { hash: '#/', reloads: 0, reload() { this.reloads++; } },
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  CSS: { escape: s => s },
  sessionStorage: { m: {}, getItem(k) { return this.m[k] ?? null; }, setItem(k, v) { this.m[k] = v; } },
  fetch: async (p, opts) => {
    calls.push(p);
    if (p === '/v1/health') return res(200, { ok: true, version: 'v1' });
    if (status !== 200) return res(status, { error: 'no' });
    const etag = 'W/"' + tag + version + '"';
    if (opts.headers['If-None-Match'] === etag) return res(304, null, etag);
    return res(200, board(), etag);
  },
  EventSource: class {
    constructor(url) { this.url = url; this.on = {}; this.closed = false; ctx.streams.push(this); }
    addEventListener(k, f) { this.on[k] = f; }
    close() { this.closed = true; }
  },
  streams: [],
};
const res = (code, data, etag = '') => ({ status: code, ok: code >= 200 && code < 300, headers: { get: () => etag }, text: async () => data ? JSON.stringify(data) : '' });
ctx.globalThis = ctx;
vm.createContext(ctx);
for (const f of ['live.js', 'app.js']) vm.runInContext(fs.readFileSync(path.join(process.argv[2], f), 'utf8'), ctx, { filename: f });
const run = code => vm.runInContext(code, ctx);
const settle = () => new Promise(r => setTimeout(r, 20));
const boards = () => calls.filter(c => c === '/v1/board').length;
const check = (name, ok) => { if (!ok) console.log(name); };

(async () => {
  await settle();
  const es = () => ctx.streams.at(-1);
  check('one stream after the first board', ctx.streams.length === 1 && boards() === 1);
  check('version from health', run('state.version') === 'v1');
  es().onopen();
  check('header live', run('state.reachable') && /^live · updated/.test(els.line.children[0] || ''));

  let n = boards();
  es().on.board({ data: run('state.etag') });
  await settle();
  check('an event naming the board shown reads nothing', boards() === n);
  tag = 'B';
  es().on.board({ data: 'W/"Bv1"' });
  await settle();
  check('an event naming another board reads it', boards() === n + 1 && run('state.etag') === 'W/"Bv1"');

  n = boards();
  es().on.ping();
  await settle();
  check('a ping checks the board', boards() === n + 1);

  // A read that ends after the stream named the board shown before: read again
  tag = 'C';
  es().on.board({ data: 'W/"Cv1"' });
  tag = 'B';
  es().on.board({ data: 'W/"Bv1"' });
  await settle();
  check('a board named back during a read is read again', run('state.etag') === 'W/"Bv1"');

  version = 'v2';
  run("state.note = 'Sent to boss-a: Merge'");
  es().on.board({ data: 'W/"Bv2"' });
  await settle();
  check('a shown note offers the reload', ctx.location.reloads === 0 && run('state.newVersion'));
  run("state.note = ''");
  tag = 'D';
  es().on.board({ data: 'W/"Dv2"' });
  await settle();
  check('a new version reloads once', ctx.location.reloads === 1);
  tag = 'E';
  es().on.board({ data: 'W/"Ev2"' });
  await settle();
  check('no second reload within a minute', ctx.location.reloads === 1 && run('state.newVersion'));

  status = 401;
  es().onerror();
  await settle();
  check('a refused stream and 401 move to Pair', ctx.location.hash === '#/pair' && run('state.unpaired') && es().closed);
  const streams = ctx.streams.length;
  status = 200;
  run('load()');
  await settle();
  check('paired again elsewhere reopens the stream', !run('state.unpaired') && ctx.streams.length === streams + 1 && !es().closed);

  const sheetText = () => run("item('boss-x', 4)")[1].children.join('');
  run("state.board = { lists: [{ boss: 'boss-x', error: 'invalid character' }] }");
  check('an item of an unreadable list says the list could not be read', /could not be read: invalid character/.test(sheetText()));
  run("state.board = { lists: [{ boss: 'boss-x', items: [] }] }");
  check('an item gone from a readable list is no longer open', /no longer open/.test(sheetText()));
  const drawn = () => els.sheet.children[1].children.join('');
  run("state.board = { lists: [{ boss: 'boss-x', error: 'invalid character' }] }; sheet({ name: 'item', boss: 'boss-x', id: 4 })");
  check('an open sheet shows the read error', /could not be read/.test(drawn()));
  run("state.board = { lists: [{ boss: 'boss-x', items: [] }] }; sheet({ name: 'item', boss: 'boss-x', id: 4 })");
  check('an open sheet redraws once the list reads again', /no longer open/.test(drawn()));
  process.exit(0);
})().catch(e => { console.log('threw: ' + e.stack); process.exit(0); });

'use strict';
// The phone page: reads /v1/board every 5 s while visible, answers, ticks and starts through /v1. List content stays in
// memory only: no storage, no service worker.

const POLL_MS = 5000;
const TIMEOUT_MS = 4000;
const TICKABLE = ['yours', 'starting', 'check', 'boss closed'];
const ON_WORKER = ['working', 'blocked', 'idle', 'gone'];
// sidebar.Groups: the board's columns.
const GROUPS = [
  { key: 'decide', title: 'To decide', statuses: ['decide'] },
  { key: 'yours', title: 'Yours', statuses: ['yours'] },
  { key: 'starting', title: 'Starting', statuses: ['starting'] },
  { key: 'progress', title: 'In progress', statuses: ON_WORKER },
  { key: 'waiting', title: 'Waiting on others', statuses: ['waiting'] },
  { key: 'check', title: 'Check', statuses: ['check'] },
  { key: 'closed', title: 'Boss closed', statuses: ['boss closed'] },
];
const FOLDED = ['waiting', 'closed'];
const CLOSED_SHOWN = 8; // Boss closed shows this many rows until "show more"
const TWO_WIDE = 9; // Yours with this many cards takes two card columns on a wide screen
const HUES = ['#2f7dd1', '#2a9d6c', '#8a6b2f', '#c2477a', '#d0802a', '#6b7f8f', '#4c8f8a', '#9b5bb0'];
const AVATARS = ['#3c6e91', '#5b7d3a', '#8c4f6e', '#7a6a2a', '#3f7f7a', '#6a5a9a'];
// A status's colour: the card's status word, as the boards colour it
const STATUS_COLOUR = { decide: 'var(--hot)', yours: 'var(--hot)', starting: 'var(--blue)', working: 'var(--green)', blocked: 'var(--red)',
  gone: 'var(--red)', idle: 'var(--grey)', waiting: 'var(--amber)', check: 'var(--amber)', 'boss closed': 'var(--grey)' };
const wide = matchMedia('(min-width: 900px)');

const state = {
  board: null, etag: '', asOf: null, reachable: true, unpaired: false,
  problem: '', // why the board isn't read, when the Mac answered but refused or sent what this page can't read
  seq: 0, // the latest board request: an older one's answer is dropped
  picks: {}, // item key → {option, text}: kept across polls
  open: {}, // lane key → whether it is unfolded
  more: false, // Boss closed shows all
  scroll: {}, // column key → its cards' scroll, kept across redraws
  filter: { project: '', on: 'anyone' },
  note: '', errors: {}, // item key → the server's last refusal
  pending: {}, // item key → an answer or tick on its way
  sig: '', sheetSig: '',
  noteFor: '', // the screen the note was left for: any other clears it
  noteShown: '', // the note last drawn: a new one is scrolled to
  opener: '', // the open sheet's hash: closing it focuses its card
  shown: null, // the open item as last drawn, {k, text}: when a poll finds it gone, its sheet closes
};

function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === 'class') e.className = v;
    // Custom properties through the CSSOM: the page's CSP refuses a style attribute
    else if (k === 'vars') for (const [p, x] of Object.entries(v)) e.style.setProperty(p, x);
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) e.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat(Infinity)) if (k != null && k !== false) e.append(k);
  return e;
}

const pad = n => String(n).padStart(2, '0');
const hhmm = d => pad(d.getHours()) + ':' + pad(d.getMinutes());
function when(unix) {
  const d = new Date(unix * 1000), now = new Date();
  if (d.toDateString() === now.toDateString()) return 'today ' + hhmm(d);
  return d.toLocaleDateString('en-GB', { weekday: 'short', day: 'numeric', month: 'short' }) + ' ' + hhmm(d);
}
// age mirrors sidebar.AgeSpan: whole minutes under two days ("18h 19m"), whole days from there.
function age(unix) {
  const m = Math.floor(Math.max(0, Date.now() / 1000 - unix) / 60);
  if (m >= 2880) return Math.floor(m / 1440) + 'd';
  if (m < 60) return m + 'm';
  return Math.floor(m / 60) + 'h' + (m % 60 ? ' ' + (m % 60) + 'm' : '');
}
const DAY = 86400;
// due is the chip for when an item ticks by itself: red once past, amber within 12 h
function due(unix) {
  const left = unix - Date.now() / 1000;
  if (left < 0) return el('span', { class: 'chip red' }, '⏱ overdue ' + age(unix));
  const d = new Date(unix * 1000), at = d.toDateString() === new Date().toDateString() ? hhmm(d) : when(unix);
  return el('span', { class: 'chip' + (left < DAY / 2 ? ' amber' : '') }, '⏱ ' + at);
}
const statusClass = s => 's-' + s.replace(/\s+/g, '-');
const hash = s => [...s].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7);
// hue is a project's colour: by its place among the board's projects, so up to 8 never share one
let hues = new Map();
const hue = name => hues.get(name);
const key = (boss, id) => boss + '/' + id;
const itemHash = (boss, id) => '#/item/' + encodeURIComponent(boss) + '/' + id;
// The project is the boss's name without boss-: the board has no project of its own.
const project = l => l.boss.startsWith('local_') ? 'desktop' : l.boss.replace(/^boss-/, '');
// A desktop app boss's list is read here, never answered, ticked or started: the Mac refuses it.
const desktop = l => l.boss.startsWith('local_');
// own is whether the item is the user's, as todo.Own has it.
const own = (l, it) => !it.on || (!it.session && it.on.toLowerCase() === l.boss.toLowerCase());
const owner = ({ list, it }) => own(list, it) ? 'you' : it.status_on || it.on;
const onWorker = ({ list, it }) => !own(list, it) && (!!it.session || ON_WORKER.includes(it.status));
const answerable = (l, it) => !desktop(l) && it.status === 'decide' && !!it.options?.length;
// A closed boss's item starts only while the manager runs, as on the tmux boards.
const startable = (l, it) => !desktop(l) && l.boss !== 'manager' && own(l, it) && !it.options?.length && (!l.closed || !!state.board?.manager_runs)
  && (['yours', 'boss closed'].includes(it.status) || !!it.start_late);
const tickable = (l, it) => !desktop(l) && !it.on && TICKABLE.includes(it.status);
const receiver = l => l.closed ? 'the manager' : l.boss;

// An option is a string "label | detail", ending " (Recommended)" for the pick the boss recommends, either side of
// " | " (as todo.Split reads it), or an object. An answer names the label.
function option(o) {
  if (typeof o !== 'string') return { label: o.label, detail: o.detail || '', rec: !!o.recommended };
  const cut = s => {
    const m = s.match(/\s*\(recommended\)$/i);
    return m ? [s.slice(0, m.index).trim(), true] : [s, false];
  };
  const [rest, rec] = cut(o);
  // Padded: an empty label or detail still splits
  const padded = ' ' + rest + ' ', i = padded.indexOf(' | ');
  const [label, recLabel] = cut(i < 0 ? rest : padded.slice(0, i).trim());
  return { label, detail: i < 0 ? '' : padded.slice(i + 3).trim(), rec: rec || recLabel };
}

// choices are an ask's options split, each with the value an answer sends: the label, or, for an older ask whose
// options share it, the option whole, to tell them apart.
function choices(it) {
  const os = it.options.map(raw => ({ raw, ...option(raw) }));
  return os.map(o => ({ ...o, value: os.filter(p => p.label === o.label).length > 1 ? o.raw : o.label }));
}

function openItems() {
  const out = [];
  for (const l of state.board.lists || []) {
    for (const it of l.items || []) if (!it.done) out.push({ list: l, it });
  }
  return out;
}

function find(boss, id) {
  const l = (state.board?.lists || []).find(l => l.boss === boss);
  const it = l?.items?.find(it => it.id === id && !it.done);
  return it ? { list: l, it } : null;
}

// call returns the answer's status, ETag and JSON body (null when it isn't JSON), the body read within the timeout;
// it throws when the Mac doesn't answer in time.
async function call(method, path, body) {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), TIMEOUT_MS);
  try {
    const headers = { 'X-Dev-Page': '1' };
    if (body) headers['Content-Type'] = 'application/json';
    if (method === 'GET' && state.etag) headers['If-None-Match'] = state.etag;
    const res = await fetch(path, { method, headers, body: body && JSON.stringify(body), cache: 'no-store', signal: ctl.signal, credentials: 'same-origin' });
    const text = await res.text();
    let data = null;
    try {
      data = JSON.parse(text);
    } catch {}
    return { status: res.status, ok: res.ok, etag: res.headers.get('ETag') || '', data };
  } finally {
    clearTimeout(timer);
  }
}

const errorOf = res => res.data?.error || 'Your Mac answered ' + res.status + '.';

function unpair() {
  state.unpaired = true;
  state.seq++; // a board read already on its way was made with the old session
  state.note = '';
  state.shown = null;
  state.board = null;
  state.etag = '';
}

async function load() {
  const seq = ++state.seq;
  let res = null;
  try {
    res = await call('GET', '/v1/board');
  } catch {}
  if (seq !== state.seq) return;
  state.reachable = !!res && (res.status === 401 || res.status === 304 || (res.ok && !!res.data));
  state.problem = !res || state.reachable ? '' : res.ok ? 'Your Mac sent a board this page cannot read.' : errorOf(res);
  if (res?.status === 401) {
    unpair();
    if (location.hash !== '#/pair') location.hash = '#/pair';
  } else if (state.reachable) {
    state.asOf = new Date();
    if (res.status !== 304) {
      state.board = res.data;
      state.etag = res.etag;
      dropStalePicks();
      const r = route();
      if (r.name === 'pair') return go('#/');
      if (r.name === 'item' && state.shown && !find(r.boss, r.id) && !unread(r.boss)) {
        return go('#/', state.errors[state.shown.k] || 'No longer open, answered or ticked on your Mac: ' + state.shown.text);
      }
    }
  }
  render();
}

const unread = boss => !!state.board?.lists?.find(l => l.boss === boss)?.error;

// go shows hash, with note on it when given.
function go(hash, note = '') {
  state.note = note;
  state.noteFor = hash;
  if (location.hash === hash) render();
  else location.hash = hash;
}

// dropStalePicks forgets a pick made on a question whose text changed since, and says so on the item.
// A list the Mac couldn't read says nothing about its items.
function dropStalePicks() {
  for (const [k, p] of Object.entries(state.picks)) {
    const i = k.lastIndexOf('/'), boss = k.slice(0, i), x = find(boss, Number(k.slice(i + 1)));
    if (!x && unread(boss)) continue;
    if (x && x.it.text === p.question && x.it.added === p.added && (!p.option || x.it.options?.some(o => o === p.option || option(o).label === p.option))) continue;
    delete state.picks[k];
    if (x && (p.option || p.text.trim())) state.errors[k] = 'The question changed since you picked: pick again.';
  }
}

// post sends body, one at a time per item; it returns the answer when the Mac took it.
async function post(path, body, k) {
  if (state.pending[k]) return;
  state.pending[k] = true;
  let res;
  try {
    res = await call('POST', path, body);
  } catch {
    res = null;
  } finally {
    delete state.pending[k];
    state.sheetSig = state.sig = ''; // redraw: the item's buttons were disabled while it was pending
  }
  if (!res) {
    state.errors[k] = 'No reply from your Mac: it may or may not have been sent. Look at the list before trying again.';
    onBoard(k, state.errors[k]);
    return load();
  }
  if (res.status === 401) {
    unpair();
    return go('#/pair', 'Not sent: pair this phone again, then try again.');
  }
  if (!res.ok) {
    state.errors[k] = (res.status === 409 && path !== '/v1/start' ? 'Already answered or changed on your Mac: ' : '') + errorOf(res);
    onBoard(k, state.errors[k]);
    return load();
  }
  return res;
}

async function send(list, it) {
  const k = key(list.boss, it.id), p = state.picks[k] || {};
  const body = { boss: list.boss, id: it.id, question: it.text };
  if (p.option) body.option = p.option; else body.text = p.text.trim();
  if (await post('/v1/answer', body, k)) {
    delete state.picks[k];
    leave(k, 'Sent to ' + list.boss + ': ' + (p.option || p.text.trim()));
  }
}

async function tick(list, it) {
  const k = key(list.boss, it.id);
  if (await post('/v1/done', { boss: list.boss, id: it.id, text: it.text }, k)) {
    leave(k, 'Ticked: ' + it.text);
  }
}

async function start(list, it) {
  const k = key(list.boss, it.id);
  if (await post('/v1/start', { boss: list.boss, id: it.id, text: it.text }, k)) {
    leave(k, 'Start sent: ' + it.text);
  }
}

// leave closes item k's sheet with note, when it is still the one shown, and reads the board again.
function leave(k, note) {
  if (state.shown?.k === k) {
    state.shown = null;
    go('#/', note);
  } else onBoard(k, note);
  load();
}

// onBoard shows item k's outcome on the board when its sheet was closed while it was on its way.
function onBoard(k, note) {
  if (state.shown?.k === k) return;
  state.note = note;
  state.noteFor = route().name === 'board' ? location.hash : '#/';
}

async function pair(code, device, out, btn) {
  out.textContent = '';
  btn.disabled = true;
  let res;
  try {
    res = await call('POST', '/v1/pair', { code: code.trim(), device: device.trim() });
  } catch {
    out.textContent = 'No reply from your Mac. If the code no longer works, run dev serve pair again.';
  }
  btn.disabled = false;
  if (!res) return;
  if (!res.ok) {
    out.textContent = errorOf(res);
    return;
  }
  state.unpaired = false;
  go('#/');
  load();
}

// statusOf is an open item's status; one the Mac couldn't work out shows as "no status".
const statusOf = it => it.status || 'no status';

function statusWord(it) {
  if (it.start_late) return el('span', { class: 'st s-decide' }, 'start not picked up');
  return el('span', { class: 'st ' + statusClass(statusOf(it)) }, statusOf(it));
}

// pick takes option value on an ask's card and opens its sheet with it ready to send.
function pick(list, it, value) {
  state.picks[key(list.boss, it.id)] = { question: it.text, added: it.added, option: value, text: '' };
  location.hash = itemHash(list.boss, it.id);
}

// band is the strip across a card's top for a state worth reading first: [text, background, ink].
function band(it) {
  if (it.start_late) return ['start not picked up', 'var(--red)', 'var(--on-red)'];
  if (it.status === 'blocked') return ['blocked · needs you', 'var(--red)', 'var(--on-red)'];
  if (it.status === 'gone') return ['worker gone', 'var(--chip-red)', 'var(--red)'];
  const c = it.check;
  // Unchanged is the Check column's reason only: elsewhere the age says it
  if (!c || c.kind === 'boss closed' || (c.kind === 'unchanged' && it.status !== 'check')) return null;
  const text = (c.pr ? 'PR #' + c.pr + ' ' : '') + c.kind + (c.since ? ' ' + age(c.since) : '');
  if (['merged', 'approved', 'checks green'].includes(c.kind)) return [text, 'var(--chip-green)', 'var(--green)'];
  if (c.kind === 'unchanged') return [text, 'var(--chip-amber)', 'var(--amber)'];
  return [text, 'var(--chip-red)', 'var(--red)'];
}

const initials = (name, proj) => name.replace(/^boss-/, '').replace(new RegExp('^' + proj.replace(/\W/g, '\\$&') + '-'), '')
  .split(/[\s._-]+/).filter(Boolean).slice(0, 2).map(w => [...w][0].toUpperCase()).join('') || [...name][0].toUpperCase();

function avatar(x) {
  if (own(x.list, x.it)) return null; // the user's: the column says so
  const name = owner(x);
  return el('span', { class: 'av', title: name, 'aria-label': 'on ' + name, vars: { '--a': AVATARS[hash(name) % AVATARS.length] } }, initials(name, project(x.list)));
}

function prChip(it) {
  if (!it.link) return null;
  const pr = it.link.match(/\/pull\/(\d+)/);
  return el('span', { class: 'chip link' }, pr ? '#' + pr[1] : 'link');
}

function label(list, it) {
  return el('div', { class: 'clab' }, el('span', { class: 'bar', vars: { '--h': hue(project(list)) } }), el('span', { class: 'pn' }, project(list)), '#' + it.id);
}

function card(x) {
  const { list, it } = x, b = band(it), h = itemHash(list.boss, it.id), k = key(list.boss, it.id);
  const lab = label(list, it);
  if (it.quiet) lab.append(el('span', { class: 'chip' }, 'TEST'));
  // The status word where neither the band nor the column says it
  if (!(it.start_late || ['decide', 'yours', 'check', 'blocked', 'gone'].includes(it.status))) lab.append(el('span', { class: 'st' }, statusOf(it)));
  const old = it.added && Date.now() / 1000 - it.added > DAY && own(list, it);
  const startBtn = startable(list, it) && el('button', { class: 'start', type: 'button', 'data-f': h + '/start', disabled: !state.reachable || state.pending[k],
    onclick: e => { e.currentTarget.disabled = true; start(list, it); } }, 'Start');
  return el('article', { class: 'card' + (it.status === 'decide' ? ' ask' : ''), vars: { '--c': STATUS_COLOUR[it.status] || 'var(--grey)' } },
    b && el('div', { class: 'band', vars: { '--bb': b[1], '--bf': b[2] } }, b[0]),
    lab,
    el('a', { class: 'title', href: h, 'data-f': h }, it.text),
    it.now && el('div', { class: 'now', title: it.now }, it.now),
    answerable(list, it) && el('div', { class: 'quick' }, choices(it).map((c, i) =>
      el('button', { type: 'button', class: c.rec ? 'rec' : '', 'data-f': h + '/' + c.value, onclick: () => pick(list, it, c.value) },
        el('span', { class: 'k' }, String(i + 1)), c.label, c.detail && el('small', null, c.detail)))),
    el('div', { class: 'foot' },
      !answerable(list, it) && it.options?.length > 0 && el('span', { class: 'chip' }, it.options.length + ' options'),
      it.until && due(it.until), prChip(it),
      it.added && el('span', { class: 'age' + (old ? ' old' : '') }, age(it.added)),
      startBtn, avatar(x)));
}

// row is a Boss closed item on one line.
function row(x) {
  const { list, it } = x, h = itemHash(list.boss, it.id);
  return el('a', { class: 'row', href: h, 'data-f': h, title: it.text },
    el('span', { class: 'sw', vars: { '--h': hue(project(list)) } }), el('span', { class: 't' }, it.text), it.added && el('span', { class: 'a' }, age(it.added)));
}

// shown is whether x passes the filters.
function shown(x) {
  const f = state.filter;
  if (f.project && project(x.list) !== f.project) return false;
  switch (f.on) {
    case 'you': return own(x.list, x.it);
    case 'workers': return onWorker(x);
    case 'people': return !own(x.list, x.it) && !onWorker(x);
  }
  return true;
}

function filters(projects) {
  const seg = (name, label, vals) => el('div', { class: 'filter' }, el('span', { class: 'flabel' }, label),
    el('div', { class: 'seg', role: 'group', 'aria-label': label }, vals.map(([v, text]) =>
      el('button', { type: 'button', 'data-f': name + ':' + v, 'aria-pressed': String(state.filter[name] === v), onclick: () => { state.filter[name] = v; render(); } },
        name === 'project' && v && el('span', { class: 'sw', vars: { '--h': hue(v) } }), text))));
  return el('div', { class: 'filters' },
    seg('project', 'Project', [['', 'All'], ...projects.map(p => [p, p])]),
    seg('on', 'On', [['anyone', 'Anyone'], ['you', 'You'], ['workers', 'Workers'], ['people', 'People']]));
}

// lanes are the open items by column; no or an unknown status is In progress, as the boards have it (NeedRow.Group).
function lanes(xs) {
  const known = GROUPS.flatMap(g => g.statuses);
  const lane = it => known.includes(it.status) ? it.status : 'working';
  // Within a lane by status, as the boards sort it (sidebar.Statuses); sort is stable
  const rank = x => known.indexOf(lane(x.it));
  return GROUPS.map(g => {
    const got = xs.filter(x => g.statuses.includes(lane(x.it))).sort((a, b) => rank(a) - rank(b));
    if (hot(g)) got.sort((a, b) => urgency(a.it, b.it));
    return { ...g, xs: got };
  });
}

// urgency orders what waits on the user: the soonest due first, then the oldest.
const urgency = (a, b) => (!a.until - !b.until) || (a.until || 0) - (b.until || 0) || (a.added || 0) - (b.added || 0);

// who is whom a lane's items are on, for its header while folded.
const who = xs => [...new Set(xs.map(x => x.it.status_on || x.it.on).filter(Boolean))].slice(0, 3).join(' · ');

const hidden = n => n > 0 && n + ' hidden by the filters';
const hot = l => l.key === 'decide' || l.key === 'yours';

function head(l, tag) {
  let meta = null;
  if (l.key === 'progress') {
    const n = s => l.xs.filter(x => x.it.status === s).length;
    const blocked = n('blocked');
    meta = el('span', { class: 'meta' }, blocked > 0 && [el('b', null, blocked + ' blocked'), ' · '], n('working') + ' working');
  }
  return el(tag, { class: 'ch', 'data-f': 'lane:' + l.key }, l.title, el('span', { class: 'cnt' }, l.xs.length), meta);
}

function body(l) {
  if (l.key === 'closed') {
    const xs = state.more ? l.xs : l.xs.slice(0, CLOSED_SHOWN);
    return [el('div', { class: 'stack', 'data-k': l.key }, xs.map(row)),
      xs.length < l.xs.length && el('button', { class: 'more', type: 'button', 'data-f': 'more', onclick: () => { state.more = true; render(); } }, 'Show ' + (l.xs.length - xs.length) + ' more')];
  }
  return [el('div', { class: 'stack', 'data-k': l.key }, l.xs.map(card)),
    l.key === 'yours' && l.xs.length > 1 && el('div', { class: 'cfoot' }, 'due first, then oldest'),
    l.hidden > 0 && el('div', { class: 'cfoot' }, hidden(l.hidden))];
}

// columns are the wide screen's board: flexible columns side by side, an empty one a strip.
function columns(ls) {
  return el('div', { class: 'board' }, ls.map(l => {
    if (!l.xs.length) {
      const why = l.title + ': ' + (hidden(l.hidden) || 'none');
      return el('section', { class: 'col empty', title: why, 'aria-label': why }, el('span', { class: 'z' }, '0'), el('span', { class: 'vl' }, l.title + (l.hidden ? ' · ' + l.hidden + ' hidden' : '')));
    }
    const cls = 'col' + (hot(l) ? ' hot' : '') + (l.key === 'closed' ? ' rows-only' : '') + (l.key === 'yours' && l.xs.length >= TWO_WIDE ? ' x2' : '');
    return el('section', { class: cls }, head(l, 'h2'), body(l));
  }));
}

// stack is the phone's board: a lane per column, Waiting on others and Boss closed folded; empty ones named on one line.
function stack(ls) {
  const empty = ls.filter(l => !l.xs.length);
  return el('div', { class: 'board' }, ls.filter(l => l.xs.length).map(l => {
    const open = state.open[l.key] ?? !FOLDED.includes(l.key);
    const h = head(l, 'summary');
    if (!open) h.append(el('span', { class: 'who' }, who(l.xs)));
    const d = el('details', { class: 'col' + (hot(l) ? ' hot' : ''), open }, h, body(l));
    d.addEventListener('toggle', () => {
      if (state.open[l.key] === d.open) return; // a lane drawn open fires toggle too
      state.open[l.key] = d.open;
      render();
    });
    return d;
  }), empty.length > 0 && el('p', { class: 'empties' }, 'Empty: ' + empty.map(l => l.title + (l.hidden ? ' (' + l.hidden + ' hidden)' : '')).join(' · ')));
}

function board() {
  const all = openItems();
  const projects = [...new Set(all.map(x => project(x.list)))].sort();
  if (!projects.includes(state.filter.project)) state.filter.project = '';
  hues = new Map(projects.map((p, i) => [p, HUES[i % HUES.length]]));
  const total = new Map(lanes(all).map(l => [l.key, l.xs.length]));
  const ls = lanes(all.filter(shown)).map(l => ({ ...l, hidden: total.get(l.key) - l.xs.length }));
  const out = [filters(projects), wide.matches ? columns(ls) : stack(ls), ...listErrors()];
  if (!all.length) out.push(el('p', { class: 'quiet' }, 'No open items.'));
  return out;
}

function listErrors() {
  return (state.board.lists || []).filter(l => l.error).map(l => el('p', { class: 'err' }, l.boss + ': its list could not be read: ' + l.error));
}

function facts(x) {
  const { list, it } = x;
  const rows = [['Status', [statusWord(it), it.status_on ? ' · ' + it.status_on : '']], ['On', owner(x)], ['List', list.boss + (list.closed ? ' (closed)' : '')]];
  if (it.link) rows.push(['Link', /^https?:/i.test(it.link) ? el('a', { href: it.link, target: '_blank', rel: 'noopener noreferrer' }, it.link) : it.link]);
  if (it.check) rows.push(['Check', it.check.kind + (it.check.pr ? ' · PR #' + it.check.pr : '')]);
  if (it.now) rows.push(['Now', it.now]);
  if (it.started) rows.push(['Started', 'sent to ' + it.started.to + ' ' + when(it.started.at)]);
  if (it.until) rows.push(['Till', when(it.until)]);
  if (it.added) rows.push(['Added', when(it.added)]);
  return el('dl', { class: 'facts' }, rows.map(([k, v]) => el('div', null, el('dt', null, k), el('dd', null, v))));
}

function answerForm(list, it) {
  const k = key(list.boss, it.id);
  const p = state.picks[k] || (state.picks[k] = { question: it.text, added: it.added, option: '', text: '' });
  const sendBtn = el('button', { class: 'btn primary', type: 'button', onclick: () => { sendBtn.disabled = true; send(list, it); } }, 'Send answer');
  const sync = () => {
    for (const b of opts.querySelectorAll('.opt')) b.setAttribute('aria-checked', b.dataset.value === p.option);
    sendBtn.disabled = !state.reachable || state.pending[k] || (!p.option && !p.text.trim());
  };
  const opts = el('div', { class: 'opts', role: 'radiogroup', 'aria-label': 'Options' }, choices(it).map((c, i) =>
    el('button', { class: 'opt', type: 'button', role: 'radio', 'data-value': c.value, onclick: () => {
      p.option = p.option === c.value ? '' : c.value;
      p.text = '';
      words.value = '';
      sync();
    } }, el('span', { class: 'k' }, String(i + 1)), el('span', null, el('span', { class: 'lab' }, c.label),
      c.rec && el('span', { class: 'rec' }, '★ recommended'), c.detail && el('span', { class: 'det' }, c.detail)))));
  const words = el('textarea', { id: 'own', rows: 3, placeholder: 'Answer…', oninput: () => {
    p.text = words.value;
    if (p.text.trim()) p.option = '';
    sync();
  } });
  words.value = p.text;
  sync();
  return [opts, el('label', { class: 'own', for: 'own' }, 'Or in your own words'), words, el('div', { class: 'acts' }, sendBtn),
    el('p', { class: 'hint' }, 'Goes to ' + list.boss + ' on your Mac and ticks the item.')];
}

const closeSheet = () => { location.hash = '#/'; };

function item(boss, id) {
  const x = find(boss, id);
  const close = el('button', { class: 'close', type: 'button', onclick: closeSheet }, 'Close');
  if (!x) {
    state.shown = null;
    return [el('div', { class: 'sh-head' }, el('span'), close), el('p', { class: 'quiet', id: 'sheet-text' }, 'This item is no longer open.')];
  }
  const { list, it } = x, k = key(boss, id);
  state.shown = { k, text: it.text };
  const out = [el('div', { class: 'sh-head' }, el('span', null, project(list) + ' #' + it.id + ' · ', statusWord(it)), close),
    el('p', { class: 'sh-text', id: 'sheet-text' }, it.text)];
  if (it.context?.length) out.push(el('ul', { class: 'context' }, it.context.map(c => el('li', null, c))));
  if (answerable(list, it)) out.push(...answerForm(list, it));
  else if (it.options?.length) out.push(el('ul', { class: 'context' }, it.options.map(option).map(c => el('li', null, c.detail ? c.label + ' — ' + c.detail : c.label))));
  if (desktop(list)) out.push(el('p', { class: 'hint' }, 'Answer it in its chat in the desktop app.'));
  if (state.errors[k]) out.push(el('p', { class: 'err' }, state.errors[k]));
  const acts = [], hints = [];
  const act = (label, cls, run, hint) => {
    const b = el('button', { class: 'btn ' + cls, type: 'button', disabled: !state.reachable || state.pending[k], onclick: () => { acts.forEach(a => { a.disabled = true; }); run(list, it); } }, label);
    acts.push(b);
    hints.push(hint);
  };
  if (startable(list, it)) act('Start', '', start, 'Start asks ' + receiver(list) + ' on your Mac to start a worker on it.');
  if (tickable(list, it)) act('Tick as done', 'primary', tick, 'Ticking tells ' + list.boss + ' on your Mac. The item leaves every board.');
  if (acts.length) {
    out.push(el('div', { class: 'acts' }, acts), el('p', { class: 'hint' }, hints.join(' ')));
  } else if (!own(list, it)) {
    out.push(el('p', { class: 'hint' }, onWorker(x) ? 'On ' + owner(x) + ': follow it in its session on your Mac.' : 'Waits on ' + owner(x) + '.'));
  }
  out.push(facts(x));
  return out;
}

function pairScreen() {
  const code = el('input', { id: 'code', autocomplete: 'one-time-code', autocapitalize: 'off', spellcheck: 'false' });
  const device = el('input', { id: 'device', value: 'iPhone' });
  const out = el('p', { class: 'err' }, state.note);
  return [el('p', null, 'On the Mac, in a terminal, run dev serve pair, then type the code it shows. It is good for 5 minutes and one use.'),
    el('label', { class: 'own', for: 'code' }, 'Code'), code, el('label', { class: 'own', for: 'device' }, 'This phone\'s name'), device,
    el('div', { class: 'acts' }, el('button', { class: 'btn primary', type: 'button', onclick: e => pair(code.value, device.value, out, e.currentTarget) }, 'Pair')), out];
}

function route() {
  const m = location.hash.match(/^#\/item\/([^/]+)\/(\d+)$/);
  if (m) {
    try {
      return { name: 'item', boss: decodeURIComponent(m[1]), id: Number(m[2]) };
    } catch {}
  }
  if (location.hash === '#/pair') return { name: 'pair' };
  return { name: 'board' };
}

// sheet draws the open item over the board, or hides it.
function sheet(r) {
  const s = document.getElementById('sheet'), on = r.name === 'item' && !!state.board;
  // Redraw only when what it shows changed, so a poll keeps a pick, typed words and focus.
  const x = on && find(r.boss, r.id);
  const sig = on ? [location.hash, JSON.stringify(x?.it), x?.list.closed, state.board?.manager_runs, state.errors[key(r.boss, r.id)], state.reachable].join('\n') : '';
  if (on && sig !== state.sheetSig) {
    if (location.hash !== state.opener) s.scrollTop = 0; // another item's sheet starts at its top
    state.opener = location.hash;
    s.replaceChildren(...item(r.boss, r.id));
  }
  state.sheetSig = sig;
  const was = s.classList.contains('on');
  s.classList.toggle('on', on);
  document.getElementById('shade').hidden = !on;
  document.getElementById('view').inert = on; // the board behind takes no tap or Tab
  document.documentElement.classList.toggle('locked', on);
  if (on && !was) s.focus({ preventScroll: true });
  else if (!on && was) {
    [...document.querySelectorAll('#view a.title, #view a.row')].find(a => a.getAttribute('href') === state.opener)?.focus({ preventScroll: true });
  }
}

function render() {
  const r = route(), b = state.board;
  document.getElementById('title').textContent = r.name === 'pair' ? 'Pair with your Mac' : 'Board';

  const line = document.getElementById('line'), tally = document.getElementById('tally');
  line.className = state.reachable ? '' : 'down';
  line.replaceChildren();
  tally.replaceChildren();
  if (!state.reachable) {
    line.append((state.problem || 'unreachable') + (state.asOf ? ' · as of ' + hhmm(state.asOf) : ''));
  } else if (b && r.name !== 'pair') {
    const c = b.counts || {}, all = openItems().filter(x => !x.it.quiet);
    const n = (count, what, cls) => el('span', { class: cls || false }, el('b', null, String(count)), what);
    tally.append(n(c.decide || 0, 'to decide', c.decide ? 'on' : ''), n(c.yours || 0, 'yours'), n(all.filter(onWorker).length, 'on workers'), n(all.length, 'open'));
    line.append('live · updated ' + hhmm(state.asOf));
  }

  // Redraw the board only when what it shows changed (ages: each minute), so a poll or the sheet keeps its scroll.
  // The pair screen keeps what was typed: it is drawn once per visit.
  const sig = r.name === 'pair' ? 'pair' : [state.reachable, state.unpaired, state.etag, state.note, JSON.stringify(state.filter), JSON.stringify(state.open), state.more, wide.matches,
    Math.floor(Date.now() / 60000)].join('\n');
  if (sig !== state.sig) {
    state.sig = sig;
    const view = document.getElementById('view');
    if (r.name === 'pair') view.replaceChildren(...pairScreen());
    else if (!b) view.replaceChildren(el('p', { class: 'quiet' }, state.reachable ? 'Loading…' : state.problem || 'Your Mac did not answer.'));
    else {
      // A redraw keeps focus on its control, and a new note is scrolled to
      const f = document.activeElement?.dataset?.f;
      // and each column keeps its scroll
      for (const st of view.querySelectorAll('.stack[data-k]')) state.scroll[st.dataset.k] = st.scrollTop;
      view.replaceChildren(...(state.note ? [el('p', { class: 'note' }, state.note)] : []), ...board());
      for (const st of view.querySelectorAll('.stack[data-k]')) st.scrollTop = state.scroll[st.dataset.k] || 0;
      if (f) view.querySelector('[data-f="' + CSS.escape(f) + '"]')?.focus({ preventScroll: true });
      if (state.note && state.note !== state.noteShown) window.scrollTo(0, 0);
      state.noteShown = state.note;
    }
  }
  sheet(r);
}

window.addEventListener('hashchange', () => {
  const r = route().name;
  if (location.hash !== state.noteFor) state.note = '';
  if (r !== 'item') state.shown = null;
  state.errors = {};
  render();
  if (r === 'pair') window.scrollTo(0, 0);
});
document.getElementById('shade').addEventListener('click', closeSheet);
document.addEventListener('keydown', e => {
  if (route().name !== 'item') return;
  if (e.key === 'Escape') return closeSheet();
  if (e.ctrlKey || e.metaKey || e.altKey || /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName)) return;
  document.querySelectorAll('.opts .opt')[Number(e.key) - 1]?.click();
});
wide.addEventListener('change', render);
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') load(); });
setInterval(() => { if (document.visibilityState === 'visible') load(); }, POLL_MS);
load();

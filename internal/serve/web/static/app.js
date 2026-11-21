'use strict';
// The phone page: reads /v1/board when /v1/events says it changed and at its pings, else every 5 s, while visible; answers, ticks and
// starts through /v1. List content stays in memory only: no service worker; sessionStorage keeps only when this tab
// last reloaded itself for a new version.

const POLL_MS = 5000;
// The stream's ping comes every 15 s: one quiet for longer than this is taken as dropped
const QUIET_MS = 40000;
const RETRY_MAX_MS = 15000;
const RELOADED = 'dev-reloaded';
const TIMEOUT_MS = 4000;
const ON_WORKER = ['working', 'blocked', 'idle', 'gone'];
const WHO_SHOWN = 10; // assignee chips shown until "+N"
const TODO_SHOWN = 5; // To do's cards shown until "Show N more", what needs the user always
// Far from the preset project colours below
const HUES = ['#c2477a', '#8a6b2f', '#4c8f8a', '#9b5bb0', '#6b7f8f'];
// The design's own project colours, by a project name's first part (nocworx-core: nocworx)
const PROJECT_HUES = { dev: 'var(--p-dev)', nocworx: 'var(--p-nocworx)', vitay: 'var(--p-vitay)' };
const AVATARS = ['#3c6e91', '#5b7d3a', '#8c4f6e', '#7a6a2a', '#3f7f7a', '#6a5a9a'];
// A mark's tone (model.js): its tag's class
const TAGS = { red: 'hot', 'soft-red': 'hot', green: 'ok', amber: 'wait', blue: 'act' };
const wide = matchMedia('(min-width: 900px)');

const state = {
  board: null, etag: '', asOf: null, reachable: true, unpaired: false,
  problem: '', // why the board isn't read, when the Mac answered but refused or sent what this page can't read
  seq: 0, // the latest board request: an older one's answer is dropped
  picks: {}, // item key → {option, text}: kept across polls
  more: false, // To do shown whole, past TODO_SHOWN
  lane: 'todo', // the phone's column in view
  allWho: false, // every assignee chip shown, past WHO_SHOWN
  scroll: {}, // column key → its cards' scroll, kept across redraws
  filter: { project: '', who: [], needs: false }, // model.js matches
  note: '', errors: {}, // item key → the server's last refusal
  pending: {}, // item key → an answer or tick on its way
  moves: {}, // item key → {to, act, after} a card dropped on column to, shown there until the board read after its call (seq > after)
  sig: '', sheetSig: '',
  noteFor: '', // the screen the note was left for: any other clears it
  noteShown: '', // the note last drawn: a new one is scrolled to
  opener: '', // the open sheet's hash: closing it focuses its card
  shown: null, // the open item as last drawn, {k, text}: when a poll finds it gone, its sheet closes
  version: '', // dev serve's version this page loaded with
  seen: '', // the version the last board named
  newVersion: false, // a newer one is out: the line offers a reload
};

// The event stream: while it is open the board is read on its events, not polled
const live = { es: null, open: false, retries: 0, retry: 0, quiet: 0, tag: '' }; // tag: the ETag the stream last named

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
const DAY = 86400;
// due is the chip for when an item ticks by itself: red once past, amber within 12 h
function due(unix) {
  const left = unix - Date.now() / 1000;
  if (left < 0) return el('span', { class: 'tag hot' }, '⏱ overdue ' + age(unix));
  const d = new Date(unix * 1000), at = d.toDateString() === new Date().toDateString() ? hhmm(d) : when(unix);
  return el('span', { class: 'tag' + (left < DAY / 2 ? ' wait' : '') }, '⏱ ' + at);
}
const statusClass = s => 's-' + s.replace(/\s+/g, '-');
const hash = s => [...s].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7);
// hue is a project's colour: by its place among the board's projects, so up to 8 never share one
let hues = new Map();
const hue = name => hues.get(name);
const key = (boss, id) => boss + '/' + id;
const itemHash = (boss, id) => '#/item/' + encodeURIComponent(boss) + '/' + id;
const owner = ({ list, it }) => own(list, it) ? 'you' : it.status_on || it.on;
const onWorker = ({ list, it }) => !own(list, it) && (!!it.session || ON_WORKER.includes(it.status));
const managerRuns = () => !!state.board?.manager_runs;

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

const NO_REPLY = 'No reply from your Mac: it may or may not have been sent. Look at the list before trying again.';
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
  const seq = ++state.seq, asked = live.tag;
  let res = null;
  try {
    res = await call('GET', '/v1/board');
  } catch {}
  if (seq !== state.seq) return;
  // A dropped card goes where the first board read after its call puts it
  if ((res?.ok && res.data) || res?.status === 304) {
    for (const [k, m] of Object.entries(state.moves)) if (m.after < seq) delete state.moves[k];
  }
  state.reachable = !!res && (res.status === 401 || res.status === 304 || (res.ok && !!res.data));
  state.problem = !res || state.reachable ? '' : res.ok ? 'Your Mac sent a board this page cannot read.' : errorOf(res);
  if (res?.status === 401) {
    unpair();
    if (location.hash !== '#/pair') location.hash = '#/pair';
  } else if (state.reachable) {
    state.asOf = new Date();
    if (state.unpaired) { // paired again, as in another tab
      state.unpaired = false;
      reconnect();
    }
    if (res.status !== 304) {
      if (!sameVersion(res.data.version)) return;
      state.board = res.data;
      state.etag = res.etag;
      // The stream named another board mid-read (one changed back): read again
      if (live.tag !== asked && live.tag && live.tag !== state.etag) setTimeout(load);
      dropStalePicks();
      const r = route();
      if (r.name === 'pair') return go('#/');
      if (r.name === 'item' && state.shown && !find(r.boss, r.id) && !unread(r.boss)) {
        return go('#/', state.errors[state.shown.k] || 'No longer open, answered or ticked on your Mac: ' + state.shown.text);
      }
    } else if (state.newVersion && !sameVersion(state.seen)) return; // a held reload, no longer busy
  }
  render();
}

// sameVersion is false when the page reloads for a new dev serve version; else true (the line may offer a reload).
function sameVersion(v) {
  let last = 0;
  try {
    last = Number(sessionStorage.getItem(RELOADED)) || 0;
  } catch {}
  const step = versionStep(state.version, v, busy(), last, Date.now());
  if (!state.version) state.version = v;
  state.seen = v;
  state.newVersion = step === 'offer';
  if (step !== 'reload') return true;
  reload();
  return false;
}

function reload() {
  try {
    sessionStorage.setItem(RELOADED, String(Date.now()));
  } catch {}
  location.reload();
}

// busy is whether the user typed or picked in the open sheet, or a note is shown: a reload would lose it.
function busy() {
  const r = route(), p = r.name === 'item' && state.picks[key(r.boss, r.id)];
  return !!state.note || (!!p && (!!p.option || !!p.text.trim()));
}

// connect opens the event stream while the page is shown and paired; each "board" event names the board's ETag.
function connect() {
  if (live.es || live.retry || document.visibilityState !== 'visible' || state.unpaired) return;
  const es = live.es = new EventSource('/v1/events');
  es.onopen = () => {
    live.open = true;
    live.retries = 0;
  };
  // While the board can't be read, every event reads it again: the ETag it last had may come back unchanged
  es.addEventListener('board', e => {
    heard();
    live.tag = e.data;
    if (e.data !== state.etag || !state.reachable) load();
  });
  // A ping checks the board (a 304 when unchanged): the stream alone doesn't prove the Mac still reads it
  es.addEventListener('ping', () => {
    heard();
    load();
  });
  // A refused stream (401) closes for good: load finds out why, and the poll takes over until a retry opens it
  es.onerror = dropped;
  heard();
}

// heard restarts the watch for a stream gone quiet: a dropped connection may fire no error.
function heard() {
  clearTimeout(live.quiet);
  live.quiet = setTimeout(dropped, QUIET_MS);
}

function dropped() {
  disconnect();
  live.retry = setTimeout(() => {
    live.retry = 0;
    connect();
  }, Math.min(RETRY_MAX_MS, 1000 * 2 ** live.retries++));
  load();
}

function disconnect() {
  live.es?.close();
  live.es = null;
  live.open = false;
  clearTimeout(live.quiet);
  clearTimeout(live.retry);
  live.retry = 0;
}

// reconnect opens the stream afresh, its retries forgotten.
function reconnect() {
  disconnect();
  live.retries = 0;
  connect();
}

const listError = boss => state.board?.lists?.find(l => l.boss === boss)?.error;
const unread = boss => !!listError(boss);

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

// post sends body, one at a time per item; it returns the answer when the Mac took it. A drop's refusal shows by the
// drop (move), not in the note at the board's top; no reply shows there too, as the list needs a look.
async function post(path, body, k) {
  if (state.pending[k]) return;
  state.pending[k] = true;
  const dropped = !!state.moves[k];
  let res;
  try {
    res = await call('POST', path, body);
  } catch {
    res = null;
  } finally {
    delete state.pending[k];
    state.sheetSig = state.sig = ''; // redraw: the item's buttons were disabled while it was pending
  }
  if (res?.ok && state.moves[k]) state.moves[k].after = state.seq;
  else delete state.moves[k];
  if (!res) {
    state.errors[k] = NO_REPLY;
    onBoard(k, state.errors[k]);
    return load();
  }
  if (res.status === 401) {
    unpair();
    return go('#/pair', 'Not sent: pair this phone again, then try again.');
  }
  if (!res.ok) {
    state.errors[k] = (res.status === 409 && ['/v1/answer', '/v1/done', '/v1/doing'].includes(path) ? 'Already answered or changed on your Mac: ' : '') + errorOf(res);
    if (!dropped) onBoard(k, state.errors[k]);
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

// itemCall posts path for item it of list with extra in its body, under k; when the Mac took it, leaves with note and
// returns it ('' when not taken).
async function itemCall(path, list, it, extra, note, k = key(list.boss, it.id)) {
  if (!await post(path, { boss: list.boss, id: it.id, text: it.text, ...extra }, k)) return '';
  leave(k, note + it.text);
  return note + it.text;
}

const tick = (list, it) => itemCall('/v1/done', list, it, {}, 'Ticked: ');
const start = (list, it) => itemCall('/v1/start', list, it, {}, 'Start sent: ');
// seen is the item's doing line and start as drawn: the Mac refuses a doing call once either changed (409)
const seen = it => ({ doing: it.doing || null, started: it.started || null });
const onIt = (list, it) => itemCall('/v1/doing', list, it, { doing: '', seen: seen(it) }, 'On it, in progress: ');
const offIt = (list, it) => itemCall('/v1/doing', list, it, { stop: true, seen: seen(it) }, 'Off it: ');
// A finished card's key: its number may be an open card's again
const doneKey = (boss, id) => 'done:' + key(boss, id);
const reopen = (list, it) => itemCall('/v1/reopen', list, it, {}, 'Reopened: ', doneKey(list.boss, it.id));
// What each verdict act calls: the sheet's buttons' own; stop doing and reopen have no button yet
const ACTS = { tick, start, doing: onIt, stop: offIt, reopen };

// leave closes item k's sheet with note, when it is still the one shown, and reads the board again; a drop's note
// move shows at the foot of the screen.
function leave(k, note) {
  if (state.moves[k]) {
    // move shows it at the foot
  } else if (state.shown?.k === k) {
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
  reconnect();
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

// initials leave out a session's project, its name's first part (dev-board-7: B7), so a project's sessions differ
function initials(name) {
  const parts = name.replace(/^boss-/, '').split(/[\s._-]+/).filter(Boolean);
  return (parts.length > 1 ? parts.slice(1) : parts).slice(0, 2).map(w => [...w][0].toUpperCase()).join('') || [...name][0].toUpperCase();
}

// avatar is an assignee's mark: ME for the user, a session's initials, a boss's square, dashed while it doesn't run; cls: an extra class.
function avatar(who, kind, running = true, cls = '') {
  if (who === ME) return el('span', { class: 'av me ' + cls, 'aria-hidden': 'true' }, 'ME');
  return el('span', { class: 'av ' + cls + (kind === 'boss' ? ' boss' : '') + (running ? '' : ' off'), 'aria-hidden': 'true',
    vars: { '--a': AVATARS[hash(who) % AVATARS.length] } }, initials(who));
}

const kindOf = x => x.it.assignee?.kind || '';

// assignee is the card's name and avatar, at its foot's end.
function assignee(x) {
  const who = whoOf(x), running = who === ME || x.done || !x.it.assignee || !!x.it.assignee.running; // running is left out when false
  const name = who === ME ? 'You' : who;
  return el('span', { class: 'asg' + (running ? '' : ' off'), title: 'assignee: ' + name + (running ? '' : ' (not running)') },
    el('span', null, name), avatar(who, kindOf(x), running));
}

function prChip(it) {
  if (!it.link) return null;
  const pr = it.link.match(/\/pull\/(\d+)/);
  return el('span', { class: 'tag link' }, pr ? '#' + pr[1] : 'link');
}

const dot = name => el('span', { class: 'pdot', vars: { '--h': hue(name) } });

// flag is what a card that needs the user says on its top.
function flag(it) {
  let why = '';
  if (it.start_late) why = ' · start not picked up';
  else if (['decide', 'blocked'].includes(it.status)) why = ' · ' + it.status;
  return el('div', { class: 'flag' }, 'Needs you' + why);
}

// card is an open item's card; a finished one is a row.
function card(x) {
  const c = x.done ? doneRow(x) : openCard(x);
  cardOf.set(c, x);
  return c;
}

// cardOf is the card each drawn card element shows, for a drag or Shift+arrow on it.
const cardOf = new WeakMap();

// What a dropped card's band says until the board settles it
const MOVING = { tick: 'Ticking…', start: 'Starting…', doing: 'Moving in progress…', stop: 'Off it…' };
const movingBand = x => x.moving && el('div', { class: 'flag pend' }, MOVING[x.moving.act]);

function openCard(x) {
  const { list, it } = x, m = marks(x), h = itemHash(list.boss, it.id), k = key(list.boss, it.id), mine = whoOf(x) === ME;
  const old = it.added && Date.now() / 1000 - it.added > DAY && mine;
  const startBtn = startable(list, it, managerRuns()) && el('button', { class: 'btn-s', type: 'button', 'data-f': h + '/start', disabled: !state.reachable || state.pending[k] || !!x.moving,
    onclick: e => { e.currentTarget.disabled = true; start(list, it); } }, 'Start');
  const [title, rest] = lead(it.text), line = m.line || (it.doing && mine ? 'on it' : '');
  // The flag says what a red band or chip would: needs you, and why
  const said = tone => m.red && tone === 'red';
  return el('article', { class: 'card' + (mine ? ' mine' : '') + (m.red ? ' need' : '') + (x.moving ? ' moving' : '') },
    movingBand(x), m.red && !x.moving && flag(it),
    el('div', { class: 'clab' }, dot(project(list)), el('span', { class: 'pn' }, project(list)), el('span', { class: 'id' }, '#' + it.id),
      m.word && el('span', { class: 'st ' + statusClass(m.word) }, m.word),
      it.added && el('span', { class: 'age' + (old ? ' old' : '') }, age(it.added))),
    el('a', { class: 'title', href: h, 'data-f': h }, title || it.text),
    title && rest && el('div', { class: 'sub' }, rest),
    line && el('div', { class: 'now', title: line }, el('b', null, it.doing && mine ? 'You, now' : 'Now'), el('span', null, line)),
    answerable(list, it) && el('div', { class: 'quick' }, choices(it).map((c, i) =>
      el('button', { type: 'button', class: c.rec ? 'rec' : '', 'data-f': h + '/' + c.value, onclick: () => pick(list, it, c.value) },
        el('span', { class: 'k' }, String(i + 1)), c.label, c.detail && el('small', null, c.detail)))),
    el('div', { class: 'foot' },
      m.band && !said(m.band[1]) && el('span', { class: 'tag ' + TAGS[m.band[1]] }, m.band[0]),
      m.chips.filter(([, tone]) => !said(tone)).map(([text, tone]) => el('span', { class: 'tag' + (tone ? ' ' + TAGS[tone] : '') }, text)),
      it.quiet && el('span', { class: 'tag test' }, 'TEST'),
      !answerable(list, it) && it.options?.length > 0 && el('span', { class: 'tag' }, it.options.length + ' options'),
      it.until && due(it.until), prChip(it),
      startBtn, assignee(x)));
}

// doneRow is a finished card on one line, read-only: no sheet, no buttons.
function doneRow(x) {
  const { list, it } = x, who = whoOf(x);
  const named = project(list) + ' #' + it.id + (it.quiet ? ', test' : '') + ', ' + (who === ME ? 'you' : who) + (it.done_at ? ', ' + age(it.done_at) + ' ago' : '') + ': ' + it.text;
  // Focusable, so a card ticked from the keyboard keeps focus
  return el('article', { class: 'card done' + (x.moving ? ' moving' : ''), title: named, 'aria-label': named, tabindex: '-1', 'data-f': 'done:' + itemHash(list.boss, it.id) },
    movingBand(x), el('span', { class: 'ok', 'aria-hidden': 'true' }, '✓'), dot(project(list)), el('span', { class: 'pn' }, project(list)), el('span', { class: 'num' }, '#' + it.id),
    el('span', { class: 't' }, it.text), it.quiet && el('span', { class: 'tag test' }, 'TEST'), prChip(it),
    it.done_at && el('span', { class: 'age' }, age(it.done_at)), avatar(who, kindOf(x), true, 'sm'));
}

// filters are the Project chips (a menu on the phone) and the Assignee avatars; those toggle, several at once, ME first.
function filters(projects, ws, kinds) {
  const f = state.filter, all = [['', 'All'], ...projects.map(p => [p, p])];
  const setProject = v => { f.project = v; refilter(); };
  const projectFilter = wide.matches
    ? el('div', { class: 'filter' }, el('span', { class: 'flabel' }, 'Project'), el('div', { class: 'seg', role: 'group', 'aria-label': 'Project' },
      all.map(([v, text]) => el('button', { type: 'button', 'data-f': 'project:' + v, 'aria-pressed': String(f.project === v), onclick: () => setProject(v) },
        v && dot(v), text))))
    : el('label', { class: 'pselect' }, el('select', { 'aria-label': 'Project', 'data-f': 'project', onchange: e => setProject(e.currentTarget.value), onblur: unpick },
      all.map(([v, text]) => el('option', { value: v, selected: f.project === v }, v ? text : 'All projects'))));
  const shown = state.allWho ? ws : ws.filter(([who], i) => i < WHO_SHOWN || f.who.includes(who)); // a pressed chip stays shown
  return el('div', { class: 'filters' }, projectFilter,
    el('div', { class: 'filter' }, el('span', { class: 'flabel' }, 'Assignee'), el('div', { class: 'avs', role: 'group', 'aria-label': 'Assignee' },
      shown.map(([who, n]) => {
        const name = who === ME ? 'Me' : who;
        return el('button', { type: 'button', class: 'avbtn' + (kinds.get(who) === 'boss' ? ' boss' : ''), 'data-f': 'who:' + who, 'aria-pressed': String(f.who.includes(who)),
          title: name + ' · ' + n + ' open', 'aria-label': name + ', ' + n + ' open',
          onclick: () => { f.who = f.who.includes(who) ? f.who.filter(w => w !== who) : [...f.who, who]; refilter(); } }, avatar(who, kinds.get(who)), el('span', { class: 'avname' }, name), el('span', { class: 'avn' + (n ? '' : ' zero') }, String(n)));
      }),
      ws.length > shown.length && el('button', { type: 'button', class: 'avmore', 'data-f': 'who:more', onclick: () => { state.allWho = true; render(); } }, '+' + (ws.length - shown.length)))));
}

// picking: a redraw kept the focused project picker (its list may be open on the phone), its options as they were:
// they are drawn afresh once it loses focus.
let picking = false;
function unpick() {
  if (!picking) return;
  picking = false;
  state.sig = '';
  setTimeout(render);
}

// redraw puts next in view; a focused project picker stays in place, the rest of its strip drawn afresh around it, as
// replacing it would close its open list.
function redraw(view, next) {
  const picker = document.activeElement?.closest?.('#view .pselect select'), keep = picker?.closest('.filters');
  const fresh = keep && next.find(n => n.classList?.contains('filters'));
  if (!fresh) return view.replaceChildren(...next);
  picking = true;
  // Its project gone meanwhile: the filter applied (All); a pick under way is left alone
  if (!fresh.querySelector('option[value="' + CSS.escape(picker.value) + '"]')) picker.value = state.filter.project;
  const nu = [...fresh.children];
  [...keep.children].forEach((c, i) => { if (!c.contains(picker) && nu[i]) c.replaceWith(nu[i]); });
  const i = next.indexOf(fresh);
  for (const c of [...view.children]) if (c !== keep) c.remove();
  keep.before(...next.slice(0, i));
  keep.after(...next.slice(i + 1));
}

const hidden = n => n > 0 && n + ' hidden by the filters';

// head is a column's square, title and count, its meta (In progress: blocked · working) at the end.
function head(l) {
  let meta = null;
  if (l.key === 'progress' && l.xs.length) {
    const n = s => l.xs.filter(x => x.it.status === s).length;
    const blocked = n('blocked');
    meta = el('span', { class: 'meta' }, blocked > 0 && [el('b', null, blocked + ' blocked'), ' · '], n('working') + ' working');
  }
  return el('h2', { class: 'ch' }, el('span', { class: 'tt' }, el('span', { class: 'sq sq-' + l.key }), l.title, el('span', { class: 'cnt' }, l.xs.length)), meta);
}

// What the tally's "wait on you" counts (todo place's needs_you)
const WAIT_ON_YOU = 'Asks to answer, your PRs that moved, starts not picked up, blocked workers';
const hotCount = l => l.xs.filter(x => needsYou(x) && !x.it.quiet).length;

function body(l) {
  // To do shows its first cards, and every one that needs the user, until opened whole
  const n = Math.max(TODO_SHOWN, l.xs.filter(needsYou).length), capped = l.key === 'todo' && l.xs.length > n;
  // and a card just dropped there wherever it sorts
  const xs = capped && !state.more ? [...l.xs.slice(0, n), ...l.xs.slice(n).filter(x => x.moving)] : l.xs;
  // Opened, focus goes to the first card it showed: the toggle moves past them
  const more = capped && el('button', { type: 'button', class: 'more', 'data-f': 'more', onclick: () => {
    state.more = !state.more;
    // Its own lane stays: the redraw goes now, not when a swipe under way stops
    clearTimeout(settling);
    settling = 0;
    render();
    if (state.more) document.querySelectorAll('#view .stack[data-k="todo"] a.title')[n]?.focus();
  } },
    state.more ? 'Show fewer' : 'Show ' + (l.xs.length - xs.length) + ' more');
  return [el('div', { class: 'stack', 'data-k': l.key }, xs.map(card), more),
    !l.xs.length && !l.hidden && el('div', { class: 'cfoot' }, 'none'),
    l.hidden > 0 && el('div', { class: 'cfoot' }, hidden(l.hidden))];
}

// columns are the wide screen's board: the five columns side by side, empty ones too.
function columns(ls) {
  return el('div', { class: 'board' }, ls.map(l => el('section', { class: 'col', 'data-k': l.key }, head(l), body(l))));
}

// swipe is the phone's board: a tab per column, with its count and what needs the user, over the columns side by side,
// one a screen, the next peeking in.
function swipe(ls) {
  // The lane is taken once a swipe stops, and a redraw held meanwhile
  const lanes = el('div', { class: 'lanes', onscroll: () => {
    clearTimeout(settling);
    settling = setTimeout(() => {
      settling = 0;
      if (lanes.isConnected) laneInView(lanes); // else the board left for Pair or Loading
      render();
    }, 150);
  } },
    ls.map(l => el('section', { class: 'lane' + (state.lane === l.key ? ' on' : ''), 'data-k': l.key, 'aria-label': l.title, inert: state.lane !== l.key }, body(l))));
  const tab = l => {
    const hot = hotCount(l);
    return el('button', { type: 'button', class: 'tab sq-' + l.key, 'data-f': 'tab:' + l.key,
      'aria-current': state.lane === l.key && 'true', onclick: () => showLane(l.key) },
    l.title, el('span', { class: 'n' }, String(l.xs.length)), hot > 0 && el('span', { class: 'hotn', title: 'wait on you' }, String(hot)));
  };
  return [el('div', { class: 'tabs', role: 'group', 'aria-label': 'Columns' }, ls.map(tab)), lanes];
}

// refilter draws the board for a filter just changed, at once: a swipe under way gives its lane first. On the phone a lane
// the filter emptied (with jump, any lane left empty: the need-you shortcut) gives way to the first drawn with cards.
function refilter(jump = false) {
  settle();
  const had = !!document.querySelector('#view .lanes > .lane[data-k="' + state.lane + '"] .card');
  render();
  const lanes = document.querySelector('#view .lanes');
  if (!(had || jump) || !lanes || lanes.querySelector('.lane.on .card')) return;
  const next = [...lanes.children].find(l => l.querySelector('.card'));
  if (next) showLane(next.dataset.k);
}

// settling is the phone lanes' scroll under way (its timer): a redraw would cut it off.
let settling = 0;

// settle ends a swipe under way at once, for a filter the user picked: its lane is the one in view.
function settle() {
  const lanes = document.querySelector('#view .lanes');
  if (!settling || !lanes) return;
  clearTimeout(settling);
  settling = 0;
  laneInView(lanes);
}

// showLane scrolls the phone's lanes to column k's.
function showLane(k) {
  const lanes = document.querySelector('#view .lanes'), lane = lanes?.querySelector('[data-k="' + k + '"]');
  if (!lane) return;
  lanes.scrollTo({ left: lane.offsetLeft - lanes.firstElementChild.offsetLeft, behavior: 'instant' });
  land(lanes, k);
}

// laneInView takes the lane most in view (the last one can't scroll to its start).
function laneInView(lanes) {
  const k = laneAt(lanes);
  if (k !== state.lane) land(lanes, k);
}

// laneAt is the key of the lane most in view.
function laneAt(lanes) {
  if (!lanes.clientWidth) return state.lane; // hidden: nothing in view
  const w = lanes.firstElementChild.offsetWidth + parseFloat(getComputedStyle(lanes).columnGap);
  const end = lanes.scrollLeft >= lanes.scrollWidth - lanes.clientWidth - 1;
  return COLUMNS[end ? COLUMNS.length - 1 : Math.max(0, Math.min(COLUMNS.length - 1, Math.round(lanes.scrollLeft / w)))].key; // a bounce scrolls past either end
}

// land makes column k's lane the one shown (the others cut to its height), its tab marked and in view, the page back up
// to the tabs when scrolled past them.
function land(lanes, k) {
  const moved = k !== state.lane, tabs = lanes.previousElementSibling;
  state.lane = k;
  // The lanes not shown out of the Tab order; focus in one that leaves goes to its tab
  const was = document.activeElement?.closest?.('.lane');
  for (const l of lanes.children) {
    l.classList.toggle('on', l.dataset.k === k);
    l.inert = l.dataset.k !== k;
  }
  if (was?.inert) document.querySelector('#view [data-f="tab:' + k + '"]')?.focus({ preventScroll: true });
  lanes.style.setProperty('--shown', lanes.querySelector('[data-k="' + k + '"]').offsetHeight + 'px');
  if (moved && tabs.getBoundingClientRect().top < 0) tabs.scrollIntoView();
  for (const t of document.querySelectorAll('#view .tab')) {
    const on = t.dataset.f === 'tab:' + k;
    if (on) t.setAttribute('aria-current', 'true');
    else t.removeAttribute('aria-current');
    if (on) {
      const strip = t.parentElement, sr = strip.getBoundingClientRect(), tr = t.getBoundingClientRect();
      strip.scrollLeft += tr.left - sr.left - (sr.width - tr.width) / 2;
    }
  }
}

// shownAt is open card x as drawn: in the column it was dropped on while that is on its way.
function shownAt(x) {
  const m = !x.done && state.moves[key(x.list.boss, x.it.id)];
  return m ? { ...x, moving: m, done: m.to === 'done', it: { ...x.it, column: m.to } } : x;
}

function board() {
  const raw = cards(state.board), all = raw.map(shownAt);
  const projects = [...new Set(all.map(x => project(x.list)))].sort();
  if (!projects.includes(state.filter.project)) state.filter.project = '';
  // A pressed assignee whose cards are gone has no chip left to unpress
  const ws = whos(all, state.filter.project);
  state.filter.who = state.filter.who.filter(w => ws.some(([k]) => k === w));
  const preset = p => PROJECT_HUES[p.split('-')[0]], others = projects.filter(p => !preset(p));
  hues = new Map(projects.map(p => [p, preset(p) || HUES[others.indexOf(p) % HUES.length]]));
  // A boss's kind wins over a card that names it by on alone
  const kinds = new Map();
  for (const x of all) if (kindOf(x) === 'boss' || !kinds.get(whoOf(x))) kinds.set(whoOf(x), kindOf(x));
  const total = new Map(lanes(all).map(l => [l.key, l.xs.length]));
  // A dropped card is filtered as it was: a tick under "wait on you" stays shown, Ticking…, until the board read after
  // its call settles it, even when a read before its reply has it ticked
  const onWay = x => !!state.moves[key(x.list.boss, x.it.id)] && !!x.list.items?.includes(x.it);
  // (a move can change its assignee, not its project)
  const ls = lanes(raw.filter(x => matches(x, state.filter) || onWay(x) && (!state.filter.project || project(x.list) === state.filter.project)).map(shownAt)).map(l => ({ ...l, hidden: total.get(l.key) - l.xs.length }));
  const out = [...listErrors(), filters(projects, ws, kinds), ...(wide.matches ? [columns(ls)] : swipe(ls))];
  if (!all.some(x => !x.done)) out.push(el('p', { class: 'quiet' }, 'No open items.'));
  return out;
}

function listErrors() {
  return (state.board.lists || []).filter(l => l.error).map(l => el('p', { class: 'err' }, l.boss + ': its list could not be read: ' + l.error.replace(/^list not read: /, '')));
}

// statusRow is the item's column, with its status after it when that adds to it ("start not picked up" overrides it):
// "yours", a person's "waiting" and "boss closed" say what the column and the rows below say.
function statusRow(x) {
  const { it } = x, col = titleOf(columnOf(x)), st = it.start_late ? 'start not picked up' : it.status;
  if (x.done || !st) return col;
  const word = ['yours', 'boss closed'].includes(st) || st === 'waiting' && it.waiting_for ? '' : st;
  // The session it names when the card is not on it (a worker gone, a closed boss's: the card is on its boss); a person is in the rows below
  const on = !it.waiting_for && it.status_on !== whoOf(x) ? it.status_on : '';
  return [col, word, on].filter(Boolean).join(' · ');
}

// whoRows say who an item is on: for one waiting on a person, that person and who follows up (the boss it is on, no one
// when that boss is closed); else the assignee.
function whoRows(x) {
  const { list, it } = x, who = whoOf(x);
  if (!it.waiting_for || kindOf(x) !== 'boss') return [['On', who === ME ? 'you' : who]];
  return [['Waiting for', it.waiting_for], ['Follows up', list.closed ? 'no one (' + who + ' is closed)' : who]];
}

function facts(x) {
  const { list, it } = x;
  const rows = [['Status', statusRow(x)], ...whoRows(x), ['List', list.boss + (list.closed ? ' (closed)' : '')]];
  if (it.link) rows.push(['Link', /^https?:/i.test(it.link) ? el('a', { href: it.link, target: '_blank', rel: 'noopener noreferrer' }, it.link) : it.link]);
  if (it.check && it.check.kind !== 'boss closed') rows.push(['Check', it.check.kind + (it.check.pr ? ' · PR #' + it.check.pr : '')]);
  if (it.now) rows.push(['Now', it.now]);
  if (it.started) rows.push(['Started', 'sent to ' + it.started.to + ' ' + when(it.started.at)]);
  if (it.until) rows.push(['Till', when(it.until)]);
  if (it.added) rows.push(['Added', when(it.added)]);
  return el('dl', { class: 'facts' }, rows.map(([k, v]) => el('div', null, el('dt', null, k), el('dd', null, v))));
}

function answerForm(list, it) {
  const k = key(list.boss, it.id);
  const p = state.picks[k] || (state.picks[k] = { question: it.text, added: it.added, option: '', text: '' });
  const sendBtn = el('button', { class: 'btn hot', type: 'button', onclick: () => { sendBtn.disabled = true; send(list, it); } }, 'Send answer');
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
    } }, el('span', { class: 'k' }, String(i + 1)), el('span', { class: 'body' }, c.rec && el('span', { class: 'rec' }, 'Recommended'),
      el('span', { class: 'lab' }, c.label), c.detail && el('span', { class: 'det' }, c.detail)))));
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
  const close = el('button', { class: 'close', type: 'button', 'aria-label': 'Close', onclick: closeSheet }, '×');
  if (!x) {
    state.shown = null;
    const err = listError(boss);
    return [el('div', { class: 'sh-head' }, el('span'), close), el('p', { class: 'quiet', id: 'sheet-text' }, err ? boss + "'s list could not be read: " + err : 'This item is no longer open.')];
  }
  const { list, it } = x, k = key(boss, id);
  state.shown = { k, text: it.text };
  const [title, rest] = lead(it.text);
  const out = [el('div', { class: 'sh-head' }, dot(project(list)), el('span', { class: 'pn' }, project(list)), el('span', null, '#' + it.id + ' ·'), statusWord(it), close)];
  if (needsYou(x)) out.push(flag(it));
  // The title, or the body when there is none, names the sheet
  if (title) out.push(el('p', { class: 'sh-text', id: 'sheet-text' }, title));
  if (rest) out.push(el('p', { class: 'sh-body', id: !title && 'sheet-text' }, rest));
  if (it.context?.length) out.push(el('ul', { class: 'context' }, it.context.map(c => el('li', null, c))));
  if (answerable(list, it)) out.push(...answerForm(list, it));
  else if (it.options?.length) out.push(el('ul', { class: 'context' }, it.options.map(option).map(c => el('li', null, c.detail ? c.label + ' — ' + c.detail : c.label))));
  if (desktop(list)) out.push(el('p', { class: 'hint' }, 'Answer it in its chat in the desktop app.'));
  if (state.errors[k]) out.push(el('p', { class: 'err' }, state.errors[k]));
  const acts = [], hints = [];
  const act = (label, cls, run, hint) => {
    const b = el('button', { class: 'btn ' + cls, type: 'button', disabled: !state.reachable || state.pending[k] || !!state.moves[k], onclick: () => { acts.forEach(a => { a.disabled = true; }); run(list, it); } }, label);
    acts.push(b);
    hints.push(hint);
  };
  if (doable(list, it)) act("I'm on it", 'primary', onIt, "I'm on it moves it to In progress as yours.");
  if (startable(list, it, managerRuns())) act('Start', '', start, 'Start asks ' + receiver(list) + ' on your Mac to start a worker on it.');
  if (tickable(list, it)) act('Tick as done', doable(list, it) ? '' : 'primary', tick, 'Ticking tells ' + list.boss + ' on your Mac. The item leaves every board.');
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
  const sig = on ? [location.hash, JSON.stringify(x?.it), x?.list.closed, state.board?.manager_runs, state.errors[key(r.boss, r.id)], !!state.moves[key(r.boss, r.id)], state.reachable, listError(r.boss)].join('\n') : '';
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
  document.getElementById('top').inert = on;
  document.documentElement.classList.toggle('locked', on);
  if (on && !was) s.focus({ preventScroll: true });
  else if (!on && was) {
    [...document.querySelectorAll('#view a.title, #view a.row')].find(a => a.getAttribute('href') === state.opener)?.focus({ preventScroll: true });
  }
}

const byF = f => '[data-f="' + CSS.escape(f) + '"]';
// twin is the data-f of a card drawn the other way: ticked (a done row), or put back (open)
const twin = f => f.startsWith('done:') ? f.slice(5) : 'done:' + f;

function render() {
  const r = route(), b = state.board;
  document.getElementById('title').textContent = r.name === 'pair' ? 'Pair with your Mac' : 'Board';

  const line = document.getElementById('line'), top = document.getElementById('tally');
  line.className = state.reachable ? '' : 'down';
  line.replaceChildren();
  const focused = top.contains(document.activeElement) && document.activeElement.dataset.f;
  top.replaceChildren();
  if (!state.reachable) {
    line.append((state.problem || 'unreachable') + (state.asOf ? ' · as of ' + hhmm(state.asOf) : ''));
  } else if (b && r.name !== 'pair') {
    const t = tally(cards(b)), f = state.filter;
    const n = (count, what) => el('span', null, el('b', null, String(count)), what);
    // What needs the user: a shortcut to those cards, again to all
    const need = el('button', { type: 'button', class: 'need' + (t.need ? ' on' : ''), 'aria-pressed': String(f.needs), 'data-f': 'needs',
      title: WAIT_ON_YOU, onclick: () => { f.needs = !f.needs; refilter(f.needs); } }, el('b', null, String(t.need)), 'wait on you');
    top.append(need, n(t.mine, 'mine'), n(t.open, 'open'));
    if (focused) need.focus({ preventScroll: true });
    line.append('live · updated ' + hhmm(state.asOf));
    if (state.newVersion) line.append(' · ', el('button', { class: 'reload', type: 'button', onclick: reload }, 'New version: reload'));
  }

  // Redraw the board only when what it shows changed (ages: each minute), so a poll or the sheet keeps its scroll.
  // The pair screen keeps what was typed: it is drawn once per visit.
  const sig = r.name === 'pair' ? 'pair' : [state.reachable, state.unpaired, state.etag, state.note, JSON.stringify(state.filter), state.more, state.allWho, wide.matches,
    JSON.stringify(state.moves), Math.floor(Date.now() / 60000)].join('\n');
  // A card lifted holds the board as it was: a redraw would drop it from under the pointer
  if (sig !== state.sig && !((settling || drag?.lifted) && b && r.name !== 'pair')) {
    state.sig = sig;
    const view = document.getElementById('view');
    if (r.name === 'pair') view.replaceChildren(...pairScreen());
    else if (!b) view.replaceChildren(el('p', { class: 'quiet' }, state.reachable ? 'Loading…' : state.problem || 'Your Mac did not answer.'));
    else {
      // A redraw keeps focus on its control, and a new note is scrolled to
      const f = document.activeElement?.dataset?.f;
      // and each column keeps its scroll, the phone's filter strip its own
      for (const st of view.querySelectorAll('.stack[data-k]')) state.scroll[st.dataset.k] = st.scrollTop;
      const strip = view.querySelector('.filters')?.scrollLeft || 0;
      redraw(view, [...(state.note ? [el('p', { class: 'note' }, state.note)] : []), ...board()]);
      for (const st of view.querySelectorAll('.stack[data-k]')) st.scrollTop = state.scroll[st.dataset.k] || 0;
      view.querySelector('.filters')?.scrollTo({ left: strip, behavior: 'instant' });
      if (!wide.matches) showLane(state.lane);
      if (f) (view.querySelector(byF(f)) || view.querySelector(byF(twin(f))))?.focus({ preventScroll: true });
      if (state.note && state.note !== state.noteShown && !dropping) window.scrollTo(0, 0);
      state.noteShown = state.note;
    }
  }
  sheet(r);
}

const FLASH_MS = 4500;
const DRAG_PX = 5; // a press moved this far lifts its card: less is a click
const titleOf = k => COLUMNS.find(c => c.key === k).title;

// acted is what of card x a drop's call reads or changes: what it is, whom it's on, its doing line and start, its column, its list's state.
const acted = ({ list, it }) => JSON.stringify([it.text, it.added, it.on, it.session?.name, it.doing, it.started, it.options, columnOf({ it }), !!list.closed]);

// move runs what dropping card x on column to means (verdict): the call the sheet's button makes (stop doing: a drop's
// only), the card shown there meanwhile; a refusal, the page's or the Mac's, puts it back and says why near at ({x, y};
// else by the card). shown: the verdict the drag showed for to, when it was lifted.
async function move(x, to, at, shown) {
  const k = x.done && !x.moving ? doneKey(x.list.boss, x.it.id) : key(x.list.boss, x.it.id);
  if (state.moves[k] || state.pending[k]) return refuse(x, 'already on its way to your Mac', at);
  if (!x.done) {
    const now = find(x.list.boss, x.it.id), was = shown || verdict(x, columnOf(x), to, managerRuns()), is = verdict(now || x, columnOf(now || x), to, managerRuns());
    if (!now) return refuse(x, 'no longer open', at);
    // A drop does what was shown when the card was picked up, not what a change on the Mac since would make of it
    if (acted(now) !== acted(x) || is?.act !== was?.act || is?.hint !== was?.hint) {
      return refuse(now, 'it changed on your Mac meanwhile: look again', at);
    }
    x = now;
  }
  const v = verdict(x, columnOf(x), to, managerRuns());
  if (!v) return;
  if (v.why) return refuse(x, v.why, at);
  // as the buttons, disabled then
  if (!state.reachable) return refuse(x, 'your Mac is not reachable', at);
  state.moves[k] = { to, act: v.act };
  delete state.errors[k];
  let said = '';
  dropping++;
  try {
    render();
    said = await ACTS[v.act](x.list, x.it);
  } finally {
    dropping--;
    if (state.moves[k]?.after === undefined) delete state.moves[k]; // never left for good
  }
  // At the foot of the screen, not in the note at the board's top: that may be scrolled away
  if (said) flash(el('div', { class: 'toast' }, said));
  else if (!state.unpaired) refuse(x, state.errors[k] || 'not moved', at, to); // unpaired: Pair says why
}

let flashTimer = 0;

// refuse says why card x stays where it is: a callout at at (else under the card) and a line at the foot, for a few seconds;
// to: the column a failed call was for, which the Mac may have saved it in anyway (its boss not told).
function refuse(x, why, at, to) {
  at ||= anchor(x);
  // Where it is now: an unanswered call may have moved it after all
  const now = x.done ? x : find(x.list.boss, x.it.id);
  const call = el('div', { class: 'refuse', role: 'alert', vars: { '--rx': Math.max(8, Math.min(at.x, innerWidth - 336)) + 'px', '--ry': Math.min(at.y, innerHeight - 140) + 'px' } }, why);
  // gone from the open items after a tick with no reply: ticked, likely; after a refusal: someone else's change
  const moved = !!to && (now ? columnOf(now) === to : to === 'done' && why === NO_REPLY);
  flash(call, el('div', { class: 'toast' }, (moved ? 'Moved, with a problem' : 'Not moved') + ' · #' + x.it.id + (now ? ' is in ' + titleOf(columnOf(now)) : ' is no longer open')));
}

// flash shows nodes (a callout, a toast at the foot) for a few seconds, in place of the last ones.
function flash(...nodes) {
  for (const n of document.querySelectorAll('body > .refuse, body > .toast')) n.remove();
  document.body.append(...nodes);
  clearTimeout(flashTimer);
  flashTimer = setTimeout(() => { for (const n of nodes) n.remove(); }, FLASH_MS);
}

// anchor is the point under card x as drawn, else the middle of the screen.
function anchor(x) {
  const r = document.querySelector('#view [data-f="' + CSS.escape(itemHash(x.list.boss, x.it.id)) + '"]')?.closest('.card')?.getBoundingClientRect();
  return r ? { x: r.left, y: r.bottom + 6 } : { x: innerWidth / 2 - 160, y: innerHeight / 2 };
}

// drag is the card pressed on the wide board, {x, el, id, sx, sy}; once moved DRAG_PX, lifted with its ghost, the
// columns (each {el, k, v: its verdict}) and the one under the pointer (over).
let drag = null, swallow = false, dropping = 0; // dropping: drops whose call is on its way

function press(e) {
  if (drag?.lifted) drop(e, true); // its pointerup never came
  swallow = false; // a new press: its click is its own
  if (e.pointerType === 'touch' || e.button !== 0 || !wide.matches || route().name !== 'board') return;
  const c = e.target.closest?.('#view .card'), x = c && cardOf.get(c);
  if (!x || x.moving || e.target.closest('button, select, input, textarea, .quick')) return;
  drag = { x, el: c, id: e.pointerId, sx: e.clientX, sy: e.clientY };
}

function lift() {
  const r = drag.el.getBoundingClientRect(), from = columnOf(drag.x);
  drag.dx = drag.sx - r.left;
  drag.dy = drag.sy - r.top;
  drag.ghost = drag.el.cloneNode(true);
  drag.ghost.classList.add('ghost');
  drag.ghost.removeAttribute('aria-label');
  drag.ghost.setAttribute('aria-hidden', 'true');
  drag.ghost.inert = true;
  drag.ghost.style.setProperty('--gw', r.width + 'px');
  document.body.append(drag.ghost);
  drag.el.classList.add('slot');
  document.documentElement.classList.add('dragging');
  getSelection()?.removeAllRanges();
  drag.cols = [...document.querySelectorAll('#view .col[data-k]')].map(c => {
    const v = verdict(drag.x, from, c.dataset.k, managerRuns());
    if (v?.why) {
      c.classList.add('is-dim');
      c.querySelector('.ch').after(el('div', { class: 'dim-hint' }, v.why));
    }
    return { el: c, k: c.dataset.k, v };
  });
  drag.lifted = true;
  drag.el.setPointerCapture?.(drag.id); // its pointerup comes back from outside the window too
}

// follow moves the ghost to the pointer and marks the column under it: a target with what a drop does, or refusing.
function follow(e) {
  drag.ghost.style.setProperty('--gx', e.clientX - drag.dx + 'px');
  drag.ghost.style.setProperty('--gy', e.clientY - drag.dy + 'px');
  const over = colAt(drag.cols, e);
  if (over === drag.over) return;
  if (drag.over) unmark(drag.over);
  drag.over = over;
  if (!over?.v) return;
  over.el.classList.add(over.v.why ? 'is-refuse' : 'is-target');
  if (over.v.act) over.el.querySelector('.ch').after(el('div', { class: 'drop-hint' }, 'Drop: ' + over.v.hint));
}

// colAt is the column of cols under the pointer of e, null for none.
const colAt = (cols, e) => cols.find(c => {
  const r = c.el.getBoundingClientRect();
  // its part on screen: a released pointer outside the window is captured too
  return e.clientX >= Math.max(0, r.left) && e.clientX <= Math.min(innerWidth, r.right) && e.clientY >= Math.max(0, r.top) && e.clientY <= Math.min(innerHeight, r.bottom);
}) || null;

function unmark(c) {
  c.el.classList.remove('is-target', 'is-refuse');
  c.el.querySelector('.drop-hint')?.remove();
}

// drop ends the drag: a lifted card goes to the column under the pointer, if any (end: no drop, as Escape).
function drop(e, end = false) {
  const d = drag;
  drag = null;
  if (!d?.lifted) return;
  // where it is released, not where it last moved; none once the board left (a sheet, the phone layout)
  const over = !end && wide.matches && route().name === 'board' && colAt(d.cols, e);
  // The click its pointerup fires opens no sheet, a cancelled drag's (button still down) too
  swallow = true;
  d.ghost.remove();
  d.el.classList.remove('slot');
  document.documentElement.classList.remove('dragging');
  for (const c of d.cols) {
    unmark(c);
    c.el.classList.remove('is-dim');
    c.el.querySelector('.dim-hint')?.remove();
  }
  render(); // what was held meanwhile
  if (over) move(d.x, over.k, { x: e.clientX, y: e.clientY + 12 }, over.v);
}

document.addEventListener('pointerdown', press);
document.addEventListener('pointermove', e => {
  if (!drag || e.pointerId !== drag.id) return;
  if (!drag.lifted) {
    if (!(e.buttons & 1)) return void (drag = null); // released where no pointerup reached the page
    if (Math.hypot(e.clientX - drag.sx, e.clientY - drag.sy) < DRAG_PX) return;
    if (!drag.el.isConnected) return void (drag = null); // redrawn since the press
    lift();
  }
  follow(e);
});
document.addEventListener('pointerup', e => {
  if (drag && e.pointerId === drag.id) drop(e);
  // its click comes before any timer; with none (released off the card), Enter's own click goes through
  if (swallow) setTimeout(() => { swallow = false; });
});
document.addEventListener('pointercancel', e => { if (drag && e.pointerId === drag.id) drop(e, true); });
window.addEventListener('blur', e => drop(e, true));
// The click that ends a drag opens no sheet
document.addEventListener('click', e => {
  if (!swallow) return;
  swallow = false;
  e.preventDefault();
  e.stopPropagation();
}, true);
// A card's link would drag as a link
document.getElementById('view').addEventListener('dragstart', e => e.preventDefault());

window.addEventListener('hashchange', () => {
  const r = route().name;
  if (r !== 'board') drop(null, true); // the board behind a sheet or Pair takes no drop
  if (location.hash !== state.noteFor) state.note = '';
  if (r !== 'item') state.shown = null;
  state.errors = {};
  render();
  if (r === 'pair') window.scrollTo(0, 0);
});
document.getElementById('shade').addEventListener('click', closeSheet);
document.addEventListener('keydown', e => {
  if (drag?.lifted && e.key === 'Escape') return drop(e, true);
  if (['Enter', ' '].includes(e.key)) swallow = false; // a drag cancelled away from the window (no pointerup): this click is its own
  if (route().name === 'board' && wide.matches && e.shiftKey && !e.repeat && !e.ctrlKey && !e.metaKey && !e.altKey && ['ArrowLeft', 'ArrowRight'].includes(e.key)) {
    const c = e.target.closest?.('#view .card'), x = c && cardOf.get(c);
    if (!x || drag?.lifted || e.target.closest('button')) return; // a card's own buttons keep their keys
    e.preventDefault();
    const next = COLUMNS[COLUMNS.findIndex(col => col.key === columnOf(x)) + (e.key === 'ArrowRight' ? 1 : -1)];
    if (next) move(x, next.key);
    return;
  }
  if (route().name !== 'item') return;
  if (e.key === 'Escape') return closeSheet();
  if (e.ctrlKey || e.metaKey || e.altKey || /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName)) return;
  document.querySelectorAll('.opts .opt')[Number(e.key) - 1]?.click();
});
wide.addEventListener('change', () => {
  drop(null, true); // the columns it was over are gone
  render();
});
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState !== 'visible') {
    drop(null, true);
    return disconnect();
  }
  load();
  reconnect();
});
setInterval(() => { if (document.visibilityState === 'visible' && (!live.open || !state.reachable)) load(); }, POLL_MS);
// The page's own version, not the first board's: that may come from a dev serve upgraded since (on the Pair screen)
call('GET', '/v1/health').then(res => { if (!state.version && res.ok) state.version = res.data?.version || ''; }).catch(() => {});
load().finally(connect);

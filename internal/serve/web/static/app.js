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
const TICKABLE = ['yours', 'starting', 'check', 'boss closed'];
const ON_WORKER = ['working', 'blocked', 'idle', 'gone'];
const WHO_SHOWN = 10; // assignee chips shown until "+N"
const TODO_SHOWN = 5; // To do's cards shown until "Show N more", what needs the user always
const HUES = ['#2f7dd1', '#2a9d6c', '#8a6b2f', '#c2477a', '#d0802a', '#6b7f8f', '#4c8f8a', '#9b5bb0'];
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
// The user marks their own item, no ask, in progress as todo.SetDoing takes it; one already marked or being started is left be.
const doable = (l, it) => !desktop(l) && own(l, it) && !it.options?.length && !it.doing && !it.started;
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
  const seq = ++state.seq, asked = live.tag;
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

async function onIt(list, it) {
  const k = key(list.boss, it.id);
  if (await post('/v1/doing', { boss: list.boss, id: it.id, text: it.text, doing: '' }, k)) {
    leave(k, 'On it, in progress: ' + it.text);
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

// avatar is an assignee's mark: ME for the user, a session's initials, a boss's square, dashed while it doesn't run; cls adds sm.
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
  if (x.done) return doneRow(x);
  const { list, it } = x, m = marks(x), h = itemHash(list.boss, it.id), k = key(list.boss, it.id), mine = whoOf(x) === ME;
  const old = it.added && Date.now() / 1000 - it.added > DAY && mine;
  const startBtn = startable(list, it) && el('button', { class: 'btn-s', type: 'button', 'data-f': h + '/start', disabled: !state.reachable || state.pending[k],
    onclick: e => { e.currentTarget.disabled = true; start(list, it); } }, 'Start');
  const [title, rest] = lead(it.text);
  // The flag says what a red band or chip would: needs you, and why
  const said = tone => m.red && tone === 'red';
  return el('article', { class: 'card' + (mine ? ' mine' : '') + (m.red ? ' need' : '') },
    m.red && flag(it),
    el('div', { class: 'clab' }, dot(project(list)), el('span', { class: 'pn' }, project(list)), el('span', { class: 'id' }, '#' + it.id),
      m.word && el('span', { class: 'st ' + statusClass(m.word) }, m.word),
      it.added && el('span', { class: 'age' + (old ? ' old' : '') }, age(it.added))),
    el('a', { class: 'title', href: h, 'data-f': h }, title || it.text),
    title && rest && el('div', { class: 'sub' }, rest),
    m.line && el('div', { class: 'now', title: m.line }, el('b', null, it.doing && mine ? 'You, now' : 'Now'), el('span', null, m.line)),
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
  return el('article', { class: 'card done', title: it.text },
    el('span', { class: 'ok', 'aria-hidden': 'true' }, '✓'), dot(project(list)), el('span', { class: 'num' }, '#' + it.id),
    el('span', { class: 't' }, lead(it.text)[0] || it.text),
    it.done_at && el('span', { class: 'age' }, age(it.done_at)), avatar(who, kindOf(x), true, 'sm'));
}

// filters are the Project chips (a menu on the phone) and the Assignee avatars; those toggle, several at once, ME first.
function filters(projects, ws, kinds) {
  const f = state.filter, all = [['', 'All'], ...projects.map(p => [p, p])];
  const setProject = v => { f.project = v; render(); };
  const projectFilter = wide.matches
    ? el('div', { class: 'filter' }, el('span', { class: 'flabel' }, 'Project'), el('div', { class: 'seg', role: 'group', 'aria-label': 'Project' },
      all.map(([v, text]) => el('button', { type: 'button', 'data-f': 'project:' + v, 'aria-pressed': String(f.project === v), onclick: () => setProject(v) },
        v && dot(v), text))))
    : el('label', { class: 'pselect' }, el('select', { 'aria-label': 'Project', 'data-f': 'project', onchange: e => setProject(e.currentTarget.value) },
      all.map(([v, text]) => el('option', { value: v, selected: f.project === v }, v ? text : 'All projects'))));
  const shown = state.allWho ? ws : ws.filter(([who], i) => i < WHO_SHOWN || f.who.includes(who)); // a pressed chip stays shown
  return el('div', { class: 'filters' }, projectFilter,
    el('div', { class: 'filter' }, el('span', { class: 'flabel' }, 'Assignee'), el('div', { class: 'avs', role: 'group', 'aria-label': 'Assignee' },
      shown.map(([who, n]) => {
        const name = who === ME ? 'Me' : who;
        return el('button', { type: 'button', class: 'avbtn' + (kinds.get(who) === 'boss' ? ' boss' : ''), 'data-f': 'who:' + who, 'aria-pressed': String(f.who.includes(who)),
          title: name + ' · ' + n, 'aria-label': name + ', ' + n + ' cards',
          onclick: () => { f.who = f.who.includes(who) ? f.who.filter(w => w !== who) : [...f.who, who]; render(); } }, avatar(who, kinds.get(who)));
      }),
      ws.length > shown.length && el('button', { type: 'button', class: 'avmore', 'data-f': 'who:more', onclick: () => { state.allWho = true; render(); } }, '+' + (ws.length - shown.length)))));
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

const hotCount = l => l.xs.filter(x => needsYou(x) && !x.it.quiet).length;

function body(l) {
  // To do shows its first cards, and every one that needs the user, until opened whole
  const capped = l.key === 'todo' && l.xs.length > TODO_SHOWN;
  const xs = capped && !state.more ? l.xs.slice(0, Math.max(TODO_SHOWN, l.xs.filter(needsYou).length)) : l.xs;
  const more = capped && el('button', { type: 'button', class: 'more', 'data-f': 'more', onclick: () => { state.more = !state.more; render(); } },
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
  const lanes = el('div', { class: 'lanes', onscroll: () => laneInView(lanes) },
    ls.map(l => el('section', { class: 'lane', 'data-k': l.key, 'aria-label': l.title }, body(l))));
  const tab = l => {
    const hot = hotCount(l);
    return el('button', { type: 'button', role: 'tab', class: 'tab sq-' + l.key, 'data-f': 'tab:' + l.key,
      'aria-selected': String(state.lane === l.key), onclick: () => showLane(l.key, true) },
    l.title, el('span', { class: 'n' }, String(l.xs.length)), hot > 0 && el('span', { class: 'hotn', title: 'need you' }, String(hot)));
  };
  return [el('div', { class: 'tabs', role: 'tablist' }, ls.map(tab)), lanes];
}

// showLane scrolls the phone's lanes to column k's.
function showLane(k, smooth) {
  state.lane = k;
  const lanes = document.querySelector('#view .lanes'), lane = lanes?.querySelector('[data-k="' + k + '"]');
  if (lane) lanes.scrollTo({ left: lane.offsetLeft - lanes.firstElementChild.offsetLeft, behavior: smooth ? 'smooth' : 'instant' });
  markTab();
}

// laneInView marks the tab of the lane swiped to.
function laneInView(lanes) {
  const first = lanes.firstElementChild, i = Math.round(lanes.scrollLeft / (first.offsetWidth + parseFloat(getComputedStyle(lanes).columnGap)));
  if (COLUMNS[i] && COLUMNS[i].key !== state.lane) {
    state.lane = COLUMNS[i].key;
    markTab();
  }
}

function markTab() {
  for (const t of document.querySelectorAll('#view .tab')) t.setAttribute('aria-selected', String(t.dataset.f === 'tab:' + state.lane));
}

function board() {
  const all = cards(state.board);
  const projects = [...new Set(all.map(x => project(x.list)))].sort();
  if (!projects.includes(state.filter.project)) state.filter.project = '';
  // A pressed assignee whose cards are gone has no chip left to unpress
  const ws = whos(all);
  state.filter.who = state.filter.who.filter(w => ws.some(([k]) => k === w));
  hues = new Map(projects.map((p, i) => [p, PROJECT_HUES[p.split('-')[0]] || HUES[i % HUES.length]]));
  const kinds = new Map(all.map(x => [whoOf(x), kindOf(x)]));
  const total = new Map(lanes(all).map(l => [l.key, l.xs.length]));
  const ls = lanes(all.filter(x => matches(x, state.filter))).map(l => ({ ...l, hidden: total.get(l.key) - l.xs.length }));
  const out = [...listErrors(), filters(projects, ws, kinds), ...(wide.matches ? [columns(ls)] : swipe(ls))];
  if (!all.some(x => !x.done)) out.push(el('p', { class: 'quiet' }, 'No open items.'));
  return out;
}

function listErrors() {
  return (state.board.lists || []).filter(l => l.error).map(l => el('p', { class: 'err' }, l.boss + ': its list could not be read: ' + l.error.replace(/^list not read: /, '')));
}

function facts(x) {
  const { list, it } = x;
  const rows = [['Status', [statusWord(it), it.status_on ? ' · ' + it.status_on : '']], ['On', whoOf(x) === ME ? 'you' : whoOf(x)], ['List', list.boss + (list.closed ? ' (closed)' : '')]];
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
    const b = el('button', { class: 'btn ' + cls, type: 'button', disabled: !state.reachable || state.pending[k], onclick: () => { acts.forEach(a => { a.disabled = true; }); run(list, it); } }, label);
    acts.push(b);
    hints.push(hint);
  };
  if (doable(list, it)) act("I'm on it", 'primary', onIt, "I'm on it moves it to In progress as yours and tells " + receiver(list) + '.');
  if (startable(list, it)) act('Start', '', start, 'Start asks ' + receiver(list) + ' on your Mac to start a worker on it.');
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
  const sig = on ? [location.hash, JSON.stringify(x?.it), x?.list.closed, state.board?.manager_runs, state.errors[key(r.boss, r.id)], state.reachable, listError(r.boss)].join('\n') : '';
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
      onclick: () => { f.needs = !f.needs; render(); } }, el('b', null, String(t.need)), 'need you');
    top.append(need, n(t.mine, 'mine'), n(t.open, 'open'));
    if (focused) need.focus({ preventScroll: true });
    line.append('live · updated ' + hhmm(state.asOf));
    if (state.newVersion) line.append(' · ', el('button', { class: 'reload', type: 'button', onclick: reload }, 'New version: reload'));
  }

  // Redraw the board only when what it shows changed (ages: each minute), so a poll or the sheet keeps its scroll.
  // The pair screen keeps what was typed: it is drawn once per visit.
  const sig = r.name === 'pair' ? 'pair' : [state.reachable, state.unpaired, state.etag, state.note, JSON.stringify(state.filter), state.more, state.allWho, wide.matches,
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
      if (!wide.matches) showLane(state.lane, false);
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
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState !== 'visible') return disconnect();
  load();
  reconnect();
});
setInterval(() => { if (document.visibilityState === 'visible' && (!live.open || !state.reachable)) load(); }, POLL_MS);
// The page's own version, not the first board's: that may come from a dev serve upgraded since (on the Pair screen)
call('GET', '/v1/health').then(res => { if (!state.version && res.ok) state.version = res.data?.version || ''; }).catch(() => {});
load().finally(connect);

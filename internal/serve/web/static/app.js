'use strict';
// The phone page: reads /v1/board every 5 s while visible, answers and ticks through /v1. List content stays in
// memory only: no storage, no service worker.

const POLL_MS = 5000;
const TIMEOUT_MS = 4000;
const TICKABLE = ['yours', 'check', 'boss closed'];
const FOLDED = ['yours', 'waiting'];

const state = {
  board: null, etag: '', asOf: null, reachable: true, unpaired: false,
  problem: '', // why the board isn't read, when the Mac answered but refused or sent what this page can't read
  seq: 0, // the latest board request: an older one's answer is dropped
  picks: {}, // item key → {option, text}: kept across polls
  open: {}, // status → whether its board group is unfolded
  note: '', errors: {}, // item key → the server's last refusal
  pending: {}, // item key → an answer or tick on its way
  sig: '',
  back: location.hash === '#/board' ? '#/board' : '#/', // the list an item was opened from
  noteFor: '', // the screen the note was left for: any other clears it
  shown: null, // the open item as last drawn, {k, text}: when a poll finds it gone, its view closes
};

function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) e.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat()) if (k != null && k !== false) e.append(k);
  return e;
}

const pad = n => String(n).padStart(2, '0');
const hhmm = d => pad(d.getHours()) + ':' + pad(d.getMinutes());
function when(unix) {
  const d = new Date(unix * 1000), now = new Date();
  if (d.toDateString() === now.toDateString()) return 'today ' + hhmm(d);
  return d.toLocaleDateString('en-GB', { weekday: 'short', day: 'numeric', month: 'short' }) + ' ' + hhmm(d);
}
const statusClass = s => 's-' + s.replace(/\s+/g, '-');
const key = (boss, id) => boss + '/' + id;
const project = l => l.project || l.boss;

// An option is a string ending in " (Recommended)" for the pick the boss recommends (as todo.Label reads it), or an object.
function option(o) {
  if (typeof o !== 'string') return { label: o.label, detail: o.detail || '', rec: !!o.recommended };
  const m = o.match(/\s*\(recommended\)$/i);
  return { label: m ? o.slice(0, m.index).trim() : o, detail: '', rec: !!m };
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
        return go(state.back, state.errors[state.shown.k] || 'No longer open, answered or ticked on your Mac: ' + state.shown.text);
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
    if (x && x.it.text === p.question && x.it.added === p.added && (!p.option || x.it.options?.some(o => option(o).label === p.option))) continue;
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
    state.sig = ''; // redraw: the item's buttons were disabled while it was pending
  }
  if (!res) {
    state.errors[k] = 'No reply from your Mac: it may or may not have been sent. Look at the list before trying again.';
    return load();
  }
  if (res.status === 401) {
    unpair();
    return go('#/pair', 'Not sent: pair this phone again, then try again.');
  }
  if (!res.ok) {
    state.errors[k] = (res.status === 409 ? 'Already answered or changed on your Mac: ' : '') + errorOf(res);
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

// leave closes item k's view with note, when it is still the one shown, and reads the board again.
function leave(k, note) {
  if (state.shown?.k === k) {
    state.shown = null;
    go(state.back, note);
  }
  load();
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
  return el('span', { class: statusClass(statusOf(it)) }, statusOf(it));
}

function row({ list, it }, withStatus) {
  const meta = [project(list)];
  if (withStatus) meta.push(' · ', statusWord(it));
  if (it.status_on) meta.push(' · ' + it.status_on);
  if (it.options?.length && !withStatus) meta.push(' · ' + it.options.length + ' options');
  return el('li', null, el('a', { class: 'row', href: '#/item/' + encodeURIComponent(list.boss) + '/' + it.id },
    el('span', { class: 'text' }, it.text), el('div', { class: 'meta' }, meta)));
}

function section(title, xs) {
  return xs.length ? [el('h2', null, title, el('span', { class: 'n' }, xs.length)), el('ul', { class: 'cells' }, xs.map(x => row(x)))] : [];
}

function forYou() {
  const all = openItems();
  const decide = all.filter(x => x.it.status === 'decide');
  const yours = all.filter(x => x.it.status === 'yours');
  const out = [];
  out.push(...section('To decide', decide), ...section('Yours', yours));
  if (!decide.length && !yours.length) out.push(el('p', { class: 'quiet' }, 'Nothing waits on you.'));
  return out.concat(listErrors());
}

function listErrors() {
  return (state.board.lists || []).filter(l => l.error).map(l => el('p', { class: 'err' }, l.boss + ': its list could not be read: ' + l.error));
}

function board() {
  const groups = new Map((state.board.statuses || []).map(s => [s, []]));
  for (const x of openItems()) {
    const s = statusOf(x.it);
    if (!groups.has(s)) groups.set(s, []);
    groups.get(s).push(x);
  }
  const out = [];
  for (const [s, xs] of groups) {
    if (!xs.length) continue;
    const d = el('details', { open: state.open[s] ?? !FOLDED.includes(s) },
      el('summary', null, el('span', { class: statusClass(s) }, s[0].toUpperCase() + s.slice(1)), ' ', el('span', { class: 'n' }, xs.length)),
      el('ul', { class: 'cells' }, xs.map(x => row(x, true))));
    d.addEventListener('toggle', () => { state.open[s] = d.open; });
    out.push(d);
  }
  out.push(...listErrors());
  if (!out.length) out.push(el('p', { class: 'quiet' }, 'No open items.'));
  return out;
}

function facts(list, it) {
  const rows = [['Status', [statusWord(it), it.status_on ? ' · ' + it.status_on : '']], ['List', list.boss]];
  if (it.link) rows.push(['Link', /^https?:/i.test(it.link) ? el('a', { href: it.link, target: '_blank', rel: 'noopener noreferrer' }, it.link) : it.link]);
  if (it.check) rows.push(['Check', it.check.kind + (it.check.pr ? ' · PR #' + it.check.pr : '')]);
  if (it.until) rows.push(['Till', when(it.until)]);
  if (it.added) rows.push(['Added', when(it.added)]);
  return el('dl', { class: 'facts' }, rows.map(([k, v]) => el('div', null, el('dt', null, k), el('dd', null, v))));
}

function answerForm(list, it) {
  const k = key(list.boss, it.id);
  const p = state.picks[k] || (state.picks[k] = { question: it.text, added: it.added, option: '', text: '' });
  const sendBtn = el('button', { class: 'act', type: 'button', onclick: () => { sendBtn.disabled = true; send(list, it); } }, 'Send answer');
  const sync = () => {
    for (const b of opts.querySelectorAll('.option')) {
      const on = b.dataset.label === p.option;
      b.setAttribute('aria-checked', on);
      b.querySelector('.mark').textContent = on ? '✓' : '';
    }
    sendBtn.disabled = !state.reachable || state.pending[k] || (!p.option && !p.text.trim());
  };
  const opts = el('ul', { class: 'cells', role: 'radiogroup', 'aria-label': 'Options' }, it.options.map(raw => {
    const o = option(raw);
    const b = el('button', { class: 'option', type: 'button', role: 'radio', 'data-label': o.label, onclick: () => {
      p.option = p.option === o.label ? '' : o.label;
      p.text = '';
      own.value = '';
      sync();
    } }, el('span', { class: 'mark' }), el('span', null, o.label, o.detail && el('span', { class: 'detail' }, o.detail), o.rec && el('span', { class: 'rec' }, '★ Recommended')));
    return el('li', null, b);
  }));
  const own = el('textarea', { id: 'own', rows: 3, placeholder: 'Answer…', oninput: () => {
    p.text = own.value;
    if (p.text.trim()) p.option = '';
    sync();
  } });
  own.value = p.text;
  sync();
  return [el('h2', null, 'Options'), opts, el('label', { class: 'own', for: 'own' }, 'Or in your own words'), own, sendBtn,
    el('p', { class: 'hint' }, 'Goes to ' + list.boss + ' on your Mac and ticks the item.')];
}

function item(boss, id) {
  const x = find(boss, id);
  const back = el('a', { class: 'back', href: state.back }, state.back === '#/board' ? '‹ Board' : '‹ For you');
  if (!x) {
    state.shown = null;
    return [back, el('p', { class: 'quiet' }, 'This item is no longer open.')];
  }
  const { list, it } = x, k = key(boss, id);
  state.shown = { k, text: it.text };
  const out = [back, el('p', { class: 'item-head' }, project(list) + ' #' + it.id), el('p', { class: 'item-text' }, it.text)];
  if (it.context?.length) out.push(el('ul', { class: 'context' }, it.context.map(c => el('li', null, c))));
  // A desktop app boss's list is read here, never answered or ticked: the Mac refuses it.
  const desktop = list.boss.startsWith('local_');
  if (desktop) {
    if (it.options?.length) out.push(el('ul', { class: 'context' }, it.options.map(o => el('li', null, option(o).label))));
    out.push(el('p', { class: 'hint' }, 'Answer it in its chat in the desktop app.'));
  }
  else if (it.status === 'decide' && it.options?.length) out.push(...answerForm(list, it));
  out.push(facts(list, it));
  if (state.errors[k]) out.push(el('p', { class: 'err' }, state.errors[k]));
  if (!desktop && !it.on && TICKABLE.includes(it.status)) {
    const b = el('button', { class: 'act', type: 'button', disabled: !state.reachable || state.pending[k], onclick: () => { b.disabled = true; tick(list, it); } }, 'Tick as done');
    out.push(b, el('p', { class: 'hint' }, 'Ticking tells ' + list.boss + ' on your Mac. The item leaves every board.'));
  }
  return out;
}

function pairScreen() {
  const code = el('input', { id: 'code', autocomplete: 'one-time-code', autocapitalize: 'off', spellcheck: 'false' });
  const device = el('input', { id: 'device', value: 'iPhone' });
  const out = el('p', { class: 'err' }, state.note);
  return [el('p', null, 'On the Mac, in a terminal, run dev serve pair, then type the code it shows. It is good for 5 minutes and one use.'),
    el('label', { class: 'own', for: 'code' }, 'Code'), code, el('label', { class: 'own', for: 'device' }, 'This phone\'s name'), device,
    el('button', { class: 'act', type: 'button', onclick: e => pair(code.value, device.value, out, e.currentTarget) }, 'Pair'), out];
}

function route() {
  const m = location.hash.match(/^#\/item\/([^/]+)\/(\d+)$/);
  if (m) {
    try {
      return { name: 'item', boss: decodeURIComponent(m[1]), id: Number(m[2]) };
    } catch {}
  }
  if (location.hash === '#/pair') return { name: 'pair' };
  if (location.hash === '#/board') return { name: 'board' };
  return { name: 'you' };
}

function render() {
  const r = route(), b = state.board;
  document.getElementById('title').textContent = { you: 'For you', board: 'Board', item: 'Item', pair: 'Pair with your Mac' }[r.name];
  document.getElementById('tab-you').classList.toggle('on', r.name === 'you');
  document.getElementById('tab-board').classList.toggle('on', r.name === 'board');
  document.getElementById('badge').textContent = b?.counts?.decide || '';

  const line = document.getElementById('line');
  line.replaceChildren();
  if (!state.reachable) {
    line.append(el('span', { class: 'down' }, (state.problem || 'unreachable') + (state.asOf ? ' · as of ' + hhmm(state.asOf) : '')));
  } else if (b) {
    const c = b.counts || {};
    line.append(el('span', { class: c.decide ? 'red' : '' }, (c.decide || 0) + ' to decide'), ' · ' + (c.yours || 0) + ' yours · updated ' + hhmm(state.asOf));
  }

  // Redraw the view only when what it shows changed, so a poll keeps a pick, typed words, focus and scroll.
  let shown = state.etag;
  if (r.name === 'item') shown = JSON.stringify(b && find(r.boss, r.id)?.it) + JSON.stringify(state.errors[key(r.boss, r.id)]);
  // The pair screen keeps what was typed: it is drawn once per visit.
  const sig = r.name === 'pair' ? location.hash : [location.hash, state.reachable, state.unpaired, shown, state.note].join('\n');
  if (sig === state.sig) return;
  state.sig = sig;
  const view = document.getElementById('view');
  if (r.name === 'pair') view.replaceChildren(...pairScreen());
  else if (!b) view.replaceChildren(el('p', { class: 'quiet' }, state.reachable ? 'Loading…' : state.problem || 'Your Mac did not answer.'));
  else if (r.name === 'item') view.replaceChildren(...item(r.boss, r.id));
  else view.replaceChildren(...(state.note ? [el('p', { class: 'note' }, state.note)] : []), ...(r.name === 'board' ? board() : forYou()));
}

window.addEventListener('hashchange', () => {
  const r = route().name;
  if (r === 'you' || r === 'board') state.back = location.hash || '#/';
  if (location.hash !== state.noteFor) state.note = '';
  if (r !== 'item') state.shown = null;
  state.errors = {};
  render();
  window.scrollTo(0, 0);
});
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') load(); });
setInterval(() => { if (document.visibilityState === 'visible') load(); }, POLL_MS);
load();

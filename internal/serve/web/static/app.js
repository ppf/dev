'use strict';
// The phone page: reads /v1/board every 5 s while visible, answers, ticks and starts through /v1. List content stays in
// memory only: no storage, no service worker.

const POLL_MS = 5000;
const TIMEOUT_MS = 4000;
const TICKABLE = ['yours', 'starting', 'check', 'boss closed'];
const ON_WORKER = ['working', 'blocked', 'idle', 'gone'];
// sidebar.Groups: the board's columns. "For you" on the phone is the first two.
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
const CLOSED_SHOWN = 2; // the desktop's Boss closed column shows this many until "show more"
const wide = matchMedia('(min-width: 900px)');

const state = {
  board: null, etag: '', asOf: null, reachable: true, unpaired: false,
  problem: '', // why the board isn't read, when the Mac answered but refused or sent what this page can't read
  seq: 0, // the latest board request: an older one's answer is dropped
  picks: {}, // item key → {option, text}: kept across polls
  open: {}, // lane key → whether it is unfolded
  more: false, // the desktop's Boss closed column shows all
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
function age(unix) {
  const s = Math.max(0, Date.now() / 1000 - unix);
  if (s < 3600) return Math.max(1, Math.round(s / 60)) + 'm';
  if (s < 86400) return Math.round(s / 3600) + 'h';
  return Math.round(s / 86400) + 'd';
}
const statusClass = s => 's-' + s.replace(/\s+/g, '-');
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
const held = it => it.status === 'decide' || it.status === 'yours';
const answerable = (l, it) => !desktop(l) && it.status === 'decide' && !!it.options?.length;
const startable = (l, it) => !desktop(l) && own(l, it) && !it.options?.length && (['yours', 'boss closed'].includes(it.status) || !!it.start_late);
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
    state.sheetSig = ''; // redraw: the item's buttons were disabled while it was pending
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

function card(x) {
  const { list, it } = x;
  const foot = [owner(x)];
  if (it.options?.length) foot.push(it.options.length + ' options');
  if (it.until) foot.push('till ' + when(it.until));
  if (it.link) {
    const pr = it.link.match(/\/pull\/(\d+)/);
    foot.push(pr ? 'PR #' + pr[1] : 'link');
  }
  if (it.added) foot.push(age(it.added));
  const cls = 'card' + (held(it) ? ' held' : '') + (it.status === 'boss closed' ? ' quiet' : '');
  return el('article', { class: cls },
    el('div', { class: 'ctop' }, el('span', { class: 'num' }, project(list) + ' #' + it.id), statusWord(it)),
    el('a', { class: 'title', href: itemHash(list.boss, it.id), 'data-f': itemHash(list.boss, it.id) }, it.text),
    answerable(list, it) && el('div', { class: 'quick' }, choices(it).map(c =>
      el('button', { type: 'button', class: c.rec ? 'rec' : '', 'data-f': itemHash(list.boss, it.id) + '/' + c.value, title: c.detail || false, onclick: () => pick(list, it, c.value) }, c.label))),
    el('div', { class: 'foot' }, foot.join(' · ')));
}

const cards = xs => xs.map(card);

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
      el('button', { type: 'button', 'data-f': name + ':' + v, 'aria-pressed': String(state.filter[name] === v), onclick: () => { state.filter[name] = v; render(); } }, text))));
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
  return GROUPS.map(g => ({ ...g, xs: xs.filter(x => g.statuses.includes(lane(x.it))).sort((a, b) => rank(a) - rank(b)) }));
}

// who is whom a lane's items are on, for its header while folded.
const who = xs => [...new Set(xs.map(x => x.it.status_on || x.it.on).filter(Boolean))].slice(0, 3).join(' · ');

const hidden = n => n > 0 && n + ' hidden by the filters';

function stack(ls) {
  const [decide, yours, ...rest] = ls;
  const forYou = el('section', { class: 'foryou' }, el('h2', null, 'For you', el('span', { class: 'n' }, decide.xs.length + yours.xs.length)),
    decide.xs.length > 0 && [el('h3', null, 'To decide · ' + decide.xs.length), cards(decide.xs)],
    yours.xs.length > 0 && [el('h3', null, 'Yours · ' + yours.xs.length), cards(yours.xs)],
    !decide.xs.length && !yours.xs.length && el('p', { class: 'quiet' }, hidden(decide.hidden + yours.hidden) || 'Nothing waits on you.'));
  return [forYou, ...rest.filter(l => l.xs.length).map(l => {
    const open = state.open[l.key] ?? !FOLDED.includes(l.key);
    const d = el('details', { class: 'lane', open },
      el('summary', { 'data-f': 'lane:' + l.key }, el('span', { class: 'lt' }, l.title), el('span', { class: 'n' }, l.xs.length), !open && el('span', { class: 'who' }, who(l.xs))),
      el('div', { class: 'cards' }, cards(l.xs)));
    d.addEventListener('toggle', () => {
      if (state.open[l.key] === d.open) return; // a lane drawn open fires toggle too
      state.open[l.key] = d.open;
      render();
    });
    return d;
  })];
}

function wall(ls) {
  const col = l => {
    const xs = l.key === 'closed' && !state.more ? l.xs.slice(0, CLOSED_SHOWN) : l.xs;
    return el('section', { class: 'col' + (l.key === 'decide' || l.key === 'yours' ? ' hot' : '') },
      el('h2', null, l.title, el('span', { class: 'n' }, l.xs.length)),
      xs.length ? cards(xs) : el('p', { class: 'quiet' }, hidden(l.hidden) || 'None'),
      xs.length < l.xs.length && el('button', { class: 'more', type: 'button', onclick: () => { state.more = true; render(); } }, 'Show ' + (l.xs.length - xs.length) + ' more'));
  };
  return [el('div', { class: 'wall' }, ls.slice(0, 4).map(col)), el('div', { class: 'wall' }, ls.slice(4).map(col))];
}

function board() {
  const all = openItems();
  const projects = [...new Set(all.map(x => project(x.list)))].sort();
  if (!projects.includes(state.filter.project)) state.filter.project = '';
  const total = new Map(lanes(all).map(l => [l.key, l.xs.length]));
  const ls = lanes(all.filter(shown)).map(l => ({ ...l, hidden: total.get(l.key) - l.xs.length }));
  const out = [filters(projects), ...(wide.matches ? wall(ls) : stack(ls)), ...listErrors()];
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
  if (desktop(list)) {
    if (it.options?.length) out.push(el('ul', { class: 'context' }, it.options.map(option).map(c => el('li', null, c.detail ? c.label + ' — ' + c.detail : c.label))));
    out.push(el('p', { class: 'hint' }, 'Answer it in its chat in the desktop app.'));
  } else if (answerable(list, it)) out.push(...answerForm(list, it));
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
  const sig = on ? [location.hash, JSON.stringify(x?.it), x?.list.closed, state.errors[key(r.boss, r.id)], state.reachable].join('\n') : '';
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
    [...document.querySelectorAll('#view a.title')].find(a => a.getAttribute('href') === state.opener)?.focus({ preventScroll: true });
  }
}

function render() {
  const r = route(), b = state.board;
  document.getElementById('title').textContent = r.name === 'pair' ? 'Pair with your Mac' : 'Board';

  const line = document.getElementById('line');
  line.replaceChildren();
  if (!state.reachable) {
    line.append(el('span', { class: 'down' }, (state.problem || 'unreachable') + (state.asOf ? ' · as of ' + hhmm(state.asOf) : '')));
  } else if (b) {
    const c = b.counts || {};
    line.append(el('span', { class: c.decide ? 'hot' : '' }, (c.decide || 0) + ' to decide'), ' · ' + (c.yours || 0) + ' yours · ' + openItems().length + ' open · updated ' + hhmm(state.asOf));
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
      view.replaceChildren(...(state.note ? [el('p', { class: 'note' }, state.note)] : []), ...board());
      if (f) view.querySelector('[data-f="' + CSS.escape(f) + '"]')?.focus();
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
document.addEventListener('keydown', e => { if (e.key === 'Escape' && route().name === 'item') closeSheet(); });
wide.addEventListener('change', render);
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') load(); });
setInterval(() => { if (document.visibilityState === 'visible') load(); }, POLL_MS);
load();

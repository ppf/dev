'use strict';
// The board's model, pure: the cards, where each sits, its marks, the filters and the tally. Columns and assignees
// come from the Mac (todo.Owner.place): the page reads them, it never works them out.

// sidebar.Columns, in board order
const COLUMNS = [
  { key: 'todo', title: 'To do' },
  { key: 'progress', title: 'In progress' },
  { key: 'waiting', title: 'Waiting' },
  { key: 'review', title: 'Review' },
  { key: 'done', title: 'Done' },
];
const ME = 'me'; // the user, as an assignee filter's key
const WORKER_WORDS = ['working', 'idle', 'waiting'];

// The project is the boss's name without boss-: the board has no project of its own.
const project = l => l.boss.startsWith('local_') ? 'desktop' : l.boss.replace(/^boss-/, '');

// age mirrors sidebar.AgeSpan: whole minutes under two days ("18h 19m"), whole days from there.
function age(unix, now = Date.now()) {
  const m = Math.floor(Math.max(0, now / 1000 - unix) / 60);
  if (m >= 2880) return Math.floor(m / 1440) + 'd';
  if (m < 60) return m + 'm';
  return Math.floor(m / 60) + 'h' + (m % 60 ? ' ' + (m % 60) + 'm' : '');
}

// cards are the board's open items and its finished ones (ticked: still in a list's items, or kept in its finished),
// each {list, it, done}. A finished card's id may be an open one's again: ids restart.
function cards(board) {
  const out = [];
  for (const l of board?.lists || []) {
    for (const it of l.items || []) out.push({ list: l, it, done: !!it.done });
    for (const it of l.finished || []) out.push({ list: l, it, done: true });
  }
  return out;
}

// columnOf is the card's column; one the Mac didn't name is In progress, as an unknown status is on the boards.
function columnOf(x) {
  if (x.done) return 'done';
  return COLUMNS.some(c => c.key === x.it.column) ? x.it.column : 'progress';
}

// whoOf is the card's assignee: ME, or a session's name.
function whoOf(x) {
  const a = x.it.assignee;
  if (!a) return x.it.on || ME;
  return a.kind === 'you' ? ME : a.name || 'boss';
}

const needsYou = x => !x.done && !!x.it.needs_you;

// marks are what a card shows beside its text: band [text, tone] across its top, word (its worker's state),
// chips [text, tone], line (what is done on it now) and red (it needs the user).
function marks(x, now = Date.now()) {
  const { list, it } = x, m = { band: null, word: '', chips: [], line: '', red: needsYou(x) };
  if (x.done) return m;
  if (it.start_late) m.band = ['start not picked up', 'red'];
  else if (it.started) m.band = ['starting · ' + it.started.to, 'blue'];
  else if (it.status === 'blocked') m.band = ['blocked · needs you', 'red'];
  else if (it.status === 'gone') m.band = ['worker ' + (it.session?.name || it.on) + ' gone', 'soft-red'];
  else m.band = checkBand(it, now);
  if (!it.status) m.word = '?'; // its boss's workers unread
  else if (it.assignee?.kind === 'worker' && WORKER_WORDS.includes(it.status)) m.word = it.status;
  if (it.status === 'decide') m.chips.push(['decide', 'red']);
  else if (m.red && !m.band?.[0].includes('needs you') && !it.start_late) m.chips.push(['needs you', 'red']);
  if (it.waiting_for) m.chips.push(['waiting for ' + it.waiting_for + (list.closed ? ' · no one follows up' : ''), 'amber']);
  if (list.closed) m.chips.push(['boss closed', '']);
  if (it.part_of) m.chips.push(['⇄ with #' + it.part_of, 'blue']);
  m.line = it.doing?.text || it.now || '';
  return m;
}

// checkBand is a PR's or a stale item's band; unchanged is a check item's reason only: elsewhere the age says it.
function checkBand(it, now) {
  const c = it.check;
  if (!c || c.kind === 'boss closed' || (c.kind === 'unchanged' && it.status !== 'check')) return null;
  const text = (c.pr ? 'PR #' + c.pr + ' ' : '') + c.kind + (c.since ? ' ' + age(c.since, now) : '');
  if (['merged', 'approved', 'checks green'].includes(c.kind)) return [text, 'green'];
  return [text, c.kind === 'unchanged' ? 'amber' : 'soft-red'];
}

// matches is whether x passes filter f: {project, who: assignee keys (none: anyone), needs: only what needs the user, as
// the tally counts it}.
const matches = (x, f) => (!f.project || project(x.list) === f.project) && (!f.who.length || f.who.includes(whoOf(x))) && (!f.needs || (needsYou(x) && !x.it.quiet));

// tally counts the open cards, test items left out: what needs the user, the user's, all.
function tally(xs) {
  const open = xs.filter(x => !x.done && !x.it.quiet);
  return { need: open.filter(needsYou).length, mine: open.filter(x => whoOf(x) === ME).length, open: open.length };
}

// whos are the assignees with their card counts: ME first, then by count, then by name.
function whos(xs) {
  const n = new Map([[ME, 0]]);
  for (const x of xs) n.set(whoOf(x), (n.get(whoOf(x)) || 0) + 1);
  return [...n].sort((a, b) => (b[0] === ME) - (a[0] === ME) || b[1] - a[1] || a[0].localeCompare(b[0]));
}

// lanes are xs by column, each in its order: Done newest first; the others what needs the user first, then (To do)
// the user's, then the soonest due, then the oldest.
function lanes(xs) {
  return COLUMNS.map(c => {
    const got = xs.filter(x => columnOf(x) === c.key);
    got.sort(c.key === 'done' ? (a, b) => (b.it.done_at || 0) - (a.it.done_at || 0)
      : (a, b) => needsYou(b) - needsYou(a) || (c.key === 'todo' && (whoOf(b) === ME) - (whoOf(a) === ME)) || urgency(a.it, b.it));
    return { ...c, xs: got };
  });
}

// urgency orders by the soonest due first, then the oldest.
const urgency = (a, b) => (!a.until - !b.until) || (a.until || 0) - (b.until || 0) || (a.added || 0) - (b.added || 0);

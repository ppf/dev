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
const ME = ':me'; // the user, as an assignee filter's key: no session name holds a colon
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
    got.sort(c.key === 'done' ? (a, b) => doneAt(b) - doneAt(a)
      : (a, b) => needsYou(b) - needsYou(a) || (c.key === 'todo' && (whoOf(b) === ME) - (whoOf(a) === ME)) || urgency(a.it, b.it));
    return { ...c, xs: got };
  });
}

// doneAt is when a card was ticked; one still in its list's items (ticked under 5 s ago) has none yet: the newest.
const doneAt = x => x.it.done_at || Infinity;

// urgency orders by the soonest due first, then the oldest.
const urgency = (a, b) => (!a.until - !b.until) || (a.until || 0) - (b.until || 0) || (a.added || 0) - (b.added || 0);

const TICKABLE = ['yours', 'starting', 'check', 'boss closed'];
// A desktop app boss's list is read here, never answered, ticked or started: the Mac refuses it.
const desktop = l => l.boss.startsWith('local_');
// own is whether the item is the user's, as todo.Own has it.
const own = (l, it) => !it.on || (!it.session && it.on.toLowerCase() === l.boss.toLowerCase());
const answerable = (l, it) => !desktop(l) && it.status === 'decide' && !!it.options?.length;
// A closed boss's item starts only while the manager runs, as on the tmux boards.
const startable = (l, it, managerRuns) => !desktop(l) && l.boss !== 'manager' && own(l, it) && !it.options?.length && (!l.closed || !!managerRuns)
  && (!!it.start_late || it.column === 'todo' && ['yours', 'boss closed'].includes(it.status)); // as todo.Start: a late start, else a To do card
const tickable = (l, it) => !desktop(l) && !it.on && TICKABLE.includes(it.status);
// The user marks their own item (the Mac's assignee: you), no ask, in progress (todo.SetDoing); the page offers it on To do
// cards only, not a desktop list's, nor one being started.
const doable = (l, it) => !desktop(l) && it.assignee?.kind === 'you' && !it.options?.length && it.column === 'todo' && !it.doing && !it.started;
const receiver = l => l.closed ? 'the manager' : l.boss;

// verdict is what dropping card x from column from on column to does: null for nothing (the same column), {act, hint}
// for the call to make (doing, stop, start, tick: the sheet's buttons' but stop), or {why} when it is refused. Waiting and Review are set
// by a session or a PR, Done reopens nothing yet; managerRuns: a closed boss's card starts only through the manager.
function verdict(x, from, to, managerRuns) {
  if (from === to) return null;
  const { list: l, it } = x, ask = !!it.options?.length, mine = whoOf(x) === ME;
  const no = why => ({ why });
  if (from === 'done') return no('reopen is not built yet');
  if (to === 'waiting') return no('set by the session');
  if (to === 'review') return no('set by the PR');
  if (desktop(l)) return no("a desktop app boss's card: change it in its chat");
  if (to === 'done') {
    if (ask) return no('answer it instead');
    if (tickable(l, it)) return { act: 'tick', hint: 'tick it as done' };
    return no(mine ? 'not tickable from the page' : l.boss + ' ticks it');
  }
  if (from === 'waiting') return no('the session moves it out of Waiting');
  if (from === 'review') return no('the PR moves it out of Review');
  if (to === 'progress') {
    if (ask) return no('answer it: open the card');
    if (doable(l, it)) return { act: 'doing', hint: "you're on it: no session starts" };
    if (l.boss === 'manager') return no("the manager's own card: ask the manager in its session");
    if (l.closed && !managerRuns) return no(l.boss + ' is closed and no manager runs to start it');
    if (!mine && it.column === 'todo') return { act: 'start', hint: receiver(l) + ' starts a worker on it' };
    return no('not startable from the page');
  }
  // In progress → To do
  if (mine && it.doing) return { act: 'stop', hint: "you're off it: To do, or Review if its PR moved" };
  if (it.started) return no(it.started.to + ' was asked to start it');
  return no((mine ? 'you are' : whoOf(x) + ' is') + ' working on it');
}

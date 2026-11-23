'use strict';
// node page.js <static dir>: runs live.js and app.js on a stub DOM, fetch and EventSource; prints one line per failed check.
const fs = require('node:fs'), path = require('node:path'), vm = require('node:vm');

function node() {
  const n = { children: [], dataset: {}, style: { setProperty(k, v) { this[k] = v; } }, hidden: false, inert: false, scrollTop: 0, className: '', textContent: '',
    classList: { set: new Set(), toggle(c, on) { on ? this.set.add(c) : this.set.delete(c); }, contains(c) { return this.set.has(c); } },
    attrs: {}, setAttribute(k, v) { n.attrs[k] = v; }, getAttribute() { return null; }, on: {}, addEventListener(k, f) { n.on[k] = f; }, focus() {},
    contains() { return false; }, append(...k) { n.children.push(...k); }, replaceChildren(...k) { n.children = k; },
    querySelectorAll() { return []; }, querySelector() { return null; } };
  return n;
}
const els = {};
let version = 'v1', tag = 'A', status = 200, text = '', lists = null, postStatus = 200, held = null, postHeld = null;
// The server's project colours (sidebar.ProjectColourOf) of the bosses here
const pc = { a: { light: '#4246c0', dark: '#9a9cf0' }, x: { light: '#086a85', dark: '#5cc8e0' } };
const board = () => {
  const ls = lists || [{ boss: 'boss-a', items: [{ id: 1, text: text || 'item ' + tag, done: false, status: 'yours' }] }];
  // Each list's project a colour, as the server gives one to every list
  const project_colours = { ...pc, ...Object.fromEntries(ls.map(l => l.boss.startsWith('local_') ? 'desktop' : l.boss.replace(/^boss-/, '')).map(p => [p, pc[p] ?? pc.a])) };
  return { version, statuses: [], counts: { decide: 0, yours: 0 }, manager_runs: false, generated: 1, project_colours, lists: ls };
};
const calls = [], posts = [], media = { matches: false, addEventListener() {} };
const ctx = {
  pc, console, Date, JSON, Math, Number, String, Object, Array, Set, Map, Promise, RegExp, Error, AbortController,
  setTimeout, clearTimeout, setInterval: () => 0,
  document: { visibilityState: 'visible', activeElement: null, documentElement: node(), getElementById: id => els[id] ||= node(),
    createElement: tag => Object.assign(node(), { tag }), addEventListener(k, f) { (ctx.docOn[k] ||= []).push(f); }, querySelectorAll: () => [], querySelector: () => null, body: node() },
  window: { addEventListener() {}, scrollTo() {} }, innerWidth: 1280, innerHeight: 800,
  location: { hash: '#/', reloads: 0, reload() { this.reloads++; } },
  matchMedia: () => media,
  CSS: { escape: s => s },
  sessionStorage: { m: {}, getItem(k) { return this.m[k] ?? null; }, setItem(k, v) { this.m[k] = v; } },
  fetch: async (p, opts) => {
    calls.push(p);
    if (p === '/v1/health') return res(200, { ok: true, version: 'v1' });
    if (opts.method === 'POST') {
      posts.push([p, opts.body]);
      if (postHeld) await new Promise(ok => postHeld.push(ok));
      if (postStatus === 0) throw new Error('no reply');
      return postStatus === 200 ? res(200, { ok: true }) : res(postStatus, { error: 'refused by the Mac' });
    }
    if (status !== 200) return res(status, { error: 'no' });
    const etag = 'W/"' + tag + version + '"';
    const r = opts.headers['If-None-Match'] === etag ? res(304, null, etag) : res(200, board(), etag);
    // held: board reads answer, as read when asked, only once let go one by one
    return held ? new Promise(ok => held.push(() => ok(r))) : r;
  },
  EventSource: class {
    constructor(url) { this.url = url; this.on = {}; this.closed = false; ctx.streams.push(this); }
    addEventListener(k, f) { this.on[k] = f; }
    close() { this.closed = true; }
  },
  streams: [], docOn: {},
};
const res = (code, data, etag = '') => ({ status: code, ok: code >= 200 && code < 300, headers: { get: () => etag }, text: async () => data ? JSON.stringify(data) : '' });
ctx.globalThis = ctx;
vm.createContext(ctx);
for (const f of ['live.js', 'model.js', 'app.js']) vm.runInContext(fs.readFileSync(path.join(process.argv[2], f), 'utf8'), ctx, { filename: f });
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
  es().on.ping();
  await settle();
  check('a held reload fires on a 304 ping once not busy', ctx.location.reloads === 1);
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

  text = 'Fix the sheet so it reads well. ' + 'The rest of it, read as body text. '.repeat(4).trim();
  tag = 'F';
  es().on.board({ data: 'W/"Fv2"' });
  await settle();
  ctx.location.hash = '#/item/boss-a/1';
  run('render()');
  const sheet = () => els.sheet.children.filter(c => /^sh-(text|body)$/.test(c.className)).map(c => [c.className, c.children[0], c.attrs.id || '']);
  check('a long text: its first sentence the title, the rest body text', JSON.stringify(sheet()) ===
    JSON.stringify([['sh-text', 'Fix the sheet so it reads well.', 'sheet-text'], ['sh-body', text.slice(32), '']]));
  text = 'word '.repeat(40).trim();
  tag = 'G';
  es().on.board({ data: 'W/"Gv2"' });
  await settle();
  run('render()');
  check('no break: body text only, naming the sheet', JSON.stringify(sheet()) === JSON.stringify([['sh-body', text, 'sheet-text']]));
  text = 'Read the plan';
  tag = 'H';
  es().on.board({ data: 'W/"Hv2"' });
  await settle();
  run('render()');
  check('a short text: a title only', JSON.stringify(sheet()) === JSON.stringify([['sh-text', text, 'sheet-text']]));
  run("state.board = { project_colours: pc, lists: [{ boss: 'boss-a', error: 'list not read: todo x: bad', items: [] }] }");
  const top = run('board()')[0];
  check('a list read error shows first, without the server prefix', top.className === 'err' && top.children.join('') === 'boss-a: its list could not be read: todo x: bad');
  const sheetText = () => run("item('boss-x', 4)")[1].children.join('');
  run("state.board = { project_colours: pc, lists: [{ boss: 'boss-x', error: 'invalid character' }] }");
  check('an item of an unreadable list says the list could not be read', /could not be read: invalid character/.test(sheetText()));
  run("state.board = { project_colours: pc, lists: [{ boss: 'boss-x', items: [] }] }");
  check('an item gone from a readable list is no longer open', /no longer open/.test(sheetText()));
  const drawn = () => els.sheet.children[1].children.join('');
  run("state.board = { project_colours: pc, lists: [{ boss: 'boss-x', error: 'invalid character' }] }; sheet({ name: 'item', boss: 'boss-x', id: 4 })");
  check('an open sheet shows the read error', /could not be read/.test(drawn()));
  run("state.board = { project_colours: pc, lists: [{ boss: 'boss-x', items: [] }] }; sheet({ name: 'item', boss: 'boss-x', id: 4 })");
  check('an open sheet redraws once the list reads again', /no longer open/.test(drawn()));

  // The fakeserve board, every kind of card, on the phone and the wide screen
  ctx.fixture = JSON.parse(fs.readFileSync(path.join(process.argv[2], '..', 'testdata', 'board.json'), 'utf8'));
  run('state.board = fixture; state.filter = { project: "", who: [], needs: false }');
  const walk = (n, f, out = []) => { if (n && typeof n === 'object') { if (f(n)) out.push(n); (n.children || []).forEach(c => walk(c, f, out)); } return out; };
  const drawnCards = () => run('board()').flatMap(n => walk(n, c => /^card\b/.test(c.className || '')));
  const lanes = () => run('board()').flatMap(n => walk(n, c => c.tag === 'section'));
  const cardCount = run('cards(state.board).length');
  const gardenDot = walk(drawnCards().find(c => walk(c, a => /^#\/item\/boss-garden\//.test(a.attrs?.href || '')).length), c => c.className === 'pdot')[0];
  check('a project dot takes the server\'s colours: ' + JSON.stringify(gardenDot?.style), gardenDot?.style['--hl'] === '#8f6ad8' && gardenDot?.style['--hd'] === '#c9b3f5');
  const todo = run("lanes(cards(state.board)).find(l => l.key === 'todo').xs");
  const shownTodo = () => walk(lanes().find(l => l.attrs['data-k'] === 'todo'), c => /^card\b/.test(c.className || ''));
  check('phone: five lanes in order', JSON.stringify(lanes().map(c => c.attrs['data-k'])) === JSON.stringify(['todo', 'progress', 'waiting', 'review', 'done']));
  check('To do shows its first ' + run('TODO_SHOWN') + ', what needs you among them', todo.length > run('TODO_SHOWN') && shownTodo().length === run('TODO_SHOWN')
    && todo.filter(x => run('needsYou')(x)).length <= run('TODO_SHOWN'));
  check('phone: every other card drawn', drawnCards().length === cardCount - todo.length + run('TODO_SHOWN'));
  run('state.more = true');
  check('phone: To do shown whole, every card drawn', drawnCards().length === cardCount);
  media.matches = true;
  check('wide: five columns in order', JSON.stringify(lanes().map(c => c.attrs['data-k'])) === JSON.stringify(['todo', 'progress', 'waiting', 'review', 'done']));
  check('wide: every card drawn', drawnCards().length === cardCount);
  check('wide: a finished card links to its read-only sheet', drawnCards().filter(c => /\bdone\b/.test(c.className)).every(c => walk(c, x => x.tag === 'a' && /^#\/done\/[^/]+\/\d+(\/\d+)?$/.test(x.attrs.href)).length === 1));
  run("state.filter.needs = true");
  check('needs you: only those cards, as many as the tally', drawnCards().length === run('tally(cards(state.board)).need'));
  run("state.filter = { project: '', who: ['gone-worker'], needs: false }; board()");
  check('a pressed assignee with no card left is dropped', run('state.filter.who.length') === 0 && drawnCards().length === cardCount);
  run("state.filter = { project: '', who: [], needs: false }");
  const needCards = drawnCards().filter(c => /\bneed\b/.test(c.className));
  check('a card that needs you is flagged, no red chip saying it again', needCards.length === run('tally(cards(state.board)).need') + run("cards(state.board).filter(x => needsYou(x) && x.it.quiet).length")
    && needCards.every(c => c.children[0].className === 'flag' && !walk(c, x => /\btag hot\b/.test(x.className || '') && /needs you|decide/.test(x.children.join(''))).length));
  const doable = run("cards(state.board).filter(x => !x.done && doable(x.list, x.it)).map(x => x.list.boss + '#' + x.it.id)");
  check("I'm on it: own To do cards, no ask, not desktop, not doing or starting: " + doable, JSON.stringify(doable) === JSON.stringify(['boss-garden#6', 'boss-garden#7', 'boss-kitchen#7', 'boss-shed#2']));
  const startable = run("cards(state.board).filter(x => !x.done && startable(x.list, x.it, state.board.manager_runs)).map(x => x.list.boss + '#' + x.it.id)");
  check('Start: To do cards (not the one marked doing in In progress): ' + startable, !startable.includes('boss-garden#12') && startable.includes('boss-garden#6'));
  const byHref = h => drawnCards().find(c => walk(c, x => x.attrs?.href === h).length);
  const textOf = n => (n.children || []).map(c => typeof c === 'string' ? c : textOf(c)).join('');
  check('the flag says why: blocked', textOf(byHref('#/item/boss-kitchen/2').children[0]) === 'Needs you · blocked');
  check('the flag says why: decide', textOf(byHref('#/item/boss-garden/4').children[0]) === 'Needs you · decide');
  check('a flagged card keeps its other tags (merged PR)', walk(byHref('#/item/boss-kitchen/5'), x => /\btag ok\b/.test(x.className || '')).length === 1);
  run("state.more = false");
  // To do of only asks past TODO_SHOWN: every one shown, no toggle
  const asks = n => ({ project_colours: pc, lists: [{ boss: 'boss-a', items: Array.from({ length: n }, (_, i) => ({ id: i + 1, text: 'ask ' + i, status: 'decide', needs_you: true, column: 'todo', options: ['a', 'b'], assignee: { kind: 'you' } }))
    .concat([{ id: 50, text: 'plain', status: 'yours', column: 'todo', assignee: { kind: 'you' } }]) }] });
  ctx.asks = asks(7);
  run('state.board = { lists: asks.lists }');
  check('a board without project colours (an older server) still draws its cards, dots grey', drawnCards().length === 7
    && walk(drawnCards()[0], c => c.className === 'pdot')[0]?.style['--hl'] === 'var(--meta)');

  run('state.board = asks');
  const toggle = () => drawnCards().length + ' ' + run('board()').flatMap(n => walk(n, c => c.className === 'more')).map(textOf).join();
  check('7 asks and a plain card: the asks shown, 1 more: ' + toggle(), toggle() === '7 Show 1 more');
  ctx.asks = asks(7).lists[0].items.slice(0, 7);
  run('state.board = { project_colours: pc, lists: [{ boss: "boss-a", items: asks }] }');
  check('only asks past TODO_SHOWN: all shown, no toggle: ' + toggle(), toggle() === '7 ');
  ctx.late = { project_colours: pc, lists: [{ boss: 'boss-a', items: [{ id: 1, text: 'late', status: 'starting', start_late: true, needs_you: true, column: 'progress', started: { at: 1, to: 'boss-a' }, assignee: { kind: 'boss', name: 'boss-a' } }] }] };
  run('state.board = late');
  check('the flag says why: a start not picked up', textOf(drawnCards()[0].children[0]) === 'Needs you · start not picked up');
  media.matches = false;

  // A drop: the button's request, the card shown in its target until a board read after it settles it; a refusal puts it back and says why
  const you = { kind: 'you' };
  const mine = (column, more) => ({ id: 1, text: 'Order the hoses', status: 'yours', column, assignee: you, ...more });
  const worker = { id: 2, text: 'Fit the hoses', status: 'working', on: 'dev-x', session: { name: 'dev-x' }, column: 'progress', assignee: { kind: 'worker', name: 'dev-x', running: true } };
  lists = [{ boss: 'boss-a', items: [mine('todo'), worker] }];
  tag = 'D1';
  es().on.board({ data: 'W/"D1v2"' });
  await settle();
  ctx.location.hash = '#/';
  run('render()');
  const laneOf = id => lanes().find(l => walk(l, c => /^card\b/.test(c.className || '') && walk(c, a => a.attrs?.href === '#/item/boss-a/' + id).length).length)?.attrs['data-k'];
  const cardFor = id => drawnCards().find(c => walk(c, a => a.attrs?.href === '#/item/boss-a/' + id).length);
  check('drop setup: #1 in To do', laneOf(1) === 'todo');
  posts.length = 0;
  lists = [{ boss: 'boss-a', items: [mine('progress', { doing: { text: '', at: 1 } }), worker] }]; // the Mac, once it took the call
  tag = 'D2';
  const moving = run("move(find('boss-a', 1), 'progress')");
  check('a drop shows the card in its target, pending: ' + laneOf(1), laneOf(1) === 'progress' && /\bmoving\b/.test(cardFor(1).className) && run("state.moves['boss-a/1'].to") === 'progress');
  await moving;
  await settle();
  check('a drop on In progress sends what I\'m on it sends: ' + JSON.stringify(posts), posts.length === 1 && posts[0][0] === '/v1/doing'
    && posts[0][1] === JSON.stringify({ boss: 'boss-a', id: 1, text: 'Order the hoses', doing: '', seen: { doing: null, started: null } }));
  const last = cls => ctx.document.body.children.filter(c => c.className === cls).at(-1)?.children.join('');
  check('a drop says how it went at the foot, not in the note at the top: ' + last('toast') + ' / ' + run('state.note'), last('toast') === 'On it, in progress: Order the hoses' && run('state.note') === '');
  check('the board read after it settles the card', !Object.keys(run('state.moves')).length && laneOf(1) === 'progress' && !/\bmoving\b/.test(cardFor(1).className));
  const dropped = posts[0];
  lists = [{ boss: 'boss-a', items: [mine('todo'), worker] }];
  tag = 'D3';
  es().on.board({ data: 'W/"D3v2"' });
  await settle();
  posts.length = 0;
  await run("onIt(find('boss-a', 1).list, find('boss-a', 1).it)");
  await settle();
  check('the button sends the same', JSON.stringify(posts[0]) === JSON.stringify(dropped));

  lists = [{ boss: 'boss-a', items: [mine('progress', { doing: { text: '', at: 1 } }), worker] }];
  tag = 'D4';
  es().on.board({ data: 'W/"D4v2"' });
  await settle();
  posts.length = 0;
  postStatus = 409;
  await run("move(find('boss-a', 1), 'todo')");
  await settle();
  check('a drop on To do of a card you are on sends stop, with the doing line seen: ' + JSON.stringify(posts), posts.length === 1 && JSON.parse(posts[0][1]).stop === true
    && JSON.stringify(JSON.parse(posts[0][1]).seen) === JSON.stringify({ doing: { text: '', at: 1 }, started: null }));
  check('a 409 puts it back, with the reason once (the callout, not the top note): ' + laneOf(1) + ' ' + last('refuse') + ' / ' + run('state.note'), laneOf(1) === 'progress' && !Object.keys(run('state.moves')).length
    && last('refuse') === 'Already answered or changed on your Mac: refused by the Mac' && last('toast') === 'Not moved · #1 is in In progress' && !run('state.note').includes('refused'));
  postStatus = 200;
  posts.length = 0;
  await run("move(find('boss-a', 2), 'done')");
  check('a refused drop sends nothing, says why, leaves the card', !posts.length && last('refuse') === 'boss-a ticks it' && laneOf(2) === 'progress');
  await run("move(find('boss-a', 2), 'waiting')");
  check('Waiting refuses: set by the session', last('refuse') === 'set by the session' && !posts.length);

  // A board read asked before the call's reply doesn't settle the card: the one asked after it does
  const parked = { id: 3, text: 'Plan the beds', status: 'idle', on: 'boss', column: 'todo', assignee: { kind: 'boss', name: 'boss-a', running: true } };
  const show = async (items, t) => {
    lists = [{ boss: 'boss-a', items }];
    tag = t;
    es().on.board({ data: 'W/"' + t + 'v2"' });
    await settle();
  };
  await show([mine('todo'), worker, parked], 'E1');
  held = [];
  tag = 'E2';
  es().on.board({ data: 'W/"E2v2"' }); // read asked now, answering the board as it is
  await settle();
  lists = [{ boss: 'boss-a', items: [mine('todo', { done: true }), worker, parked] }];
  tag = 'E3';
  posts.length = 0;
  const ticking = run("move(find('boss-a', 1), 'done')");
  const doneCard = () => walk(lanes().find(l => l.attrs['data-k'] === 'done'), c => /\bmoving\b/.test(c.className || ''))[0];
  check('a drop on Done: pending as a done row, Ticking…', /^card done moving/.test(doneCard()?.className) && textOf(doneCard()).startsWith('Ticking…'));
  await ticking;
  await settle();
  check('a drop on Done sends the Tick button\'s request', posts.length === 1 && posts[0][0] === '/v1/done' && posts[0][1] === JSON.stringify({ boss: 'boss-a', id: 1, text: 'Order the hoses' }));
  held.shift()();
  await settle();
  check('a read asked before the reply leaves it pending', !!run("state.moves['boss-a/1']"));
  while (held.length) held.shift()();
  held = null;
  await settle();
  check('the read asked after it settles it', !run("state.moves['boss-a/1']") && !doneCard());
  const ticked = posts[0];
  await show([mine('todo'), worker, parked], 'E4');
  posts.length = 0;
  await run("tick(find('boss-a', 1).list, find('boss-a', 1).it)");
  check('the Tick button sends the same', JSON.stringify(posts[0]) === JSON.stringify(ticked));

  await show([mine('todo'), worker, parked], 'E5');
  posts.length = 0;
  await run("move(find('boss-a', 3), 'progress')");
  const started = posts[0];
  await show([mine('todo'), worker, parked], 'E6');
  posts.length = 0;
  await run("start(find('boss-a', 3).list, find('boss-a', 3).it)");
  check('a boss\'s card on In progress sends what Start sends: ' + JSON.stringify(started), started?.[0] === '/v1/start' && JSON.stringify(posts[0]) === JSON.stringify(started));

  await show([mine('todo'), worker, parked], 'E7');
  posts.length = 0;
  const first = run("move(find('boss-a', 1), 'progress')");
  await run("move(find('boss-a', 1), 'done')");
  check('a second drop while the first is on its way: refused, nothing sent', last('refuse') === 'already on its way to your Mac');
  await first;
  await settle();
  check('only the first call went', posts.length === 1);

  await show([mine('todo'), worker, parked], 'E8');
  posts.length = 0;
  postStatus = 0;
  await run("move(find('boss-a', 1), 'progress')");
  await settle();
  check('no reply: back in To do, says so', /^No reply from your Mac/.test(last('refuse')) && laneOf(1) === 'todo' && !Object.keys(run('state.moves')).length);
  postStatus = 200;
  posts.length = 0;
  run('state.reachable = false');
  await run("move(find('boss-a', 1), 'progress')");
  check('unreachable: refused, nothing sent', last('refuse') === 'your Mac is not reachable' && !posts.length);
  run('state.reachable = true');

  // Given to its boss on the Mac while lifted: the drop shown (I'm on it) isn't turned into a Start
  await show([mine('todo'), worker, parked], 'E9');
  run("globalThis.lifted = find('boss-a', 1)");
  await show([{ ...mine('todo'), on: 'boss', status: 'idle', assignee: { kind: 'boss', name: 'boss-a', running: true } }, worker, parked], 'E10');
  posts.length = 0;
  await run("move(lifted, 'progress')");
  check('changed on the Mac while lifted: refused, nothing sent', last('refuse') === 'it changed on your Mac meanwhile: look again' && !posts.length);
  await show([mine('todo'), worker, parked], 'E9b');
  run("globalThis.lifted = find('boss-a', 1)");
  await show([{ ...mine('todo'), text: 'Order the pump' }, worker, parked], 'E9c');
  await run("move(lifted, 'done')");
  await run("move(find('boss-a', 3), 'progress', null, { why: 'boss-a is closed and no manager runs to start it' })");
  check('shown refused at the lift, startable at the drop: refused, nothing sent', last('refuse') === 'it changed on your Mac meanwhile: look again' && !posts.length);
  check('reworded on the Mac while lifted: refused, nothing sent', last('refuse') === 'it changed on your Mac meanwhile: look again' && !posts.length);

  // Under "need you", a ticked Review card stays shown, Ticking…, until settled
  await show([mine('review', { status: 'check', needs_you: true }), worker], 'E11');
  run('state.filter.needs = true');
  held = [];
  const tickReview = run("move(find('boss-a', 1), 'done')");
  check('need you: a ticked card stays shown, Ticking…', !!doneCard());
  await tickReview;
  while (held.length) held.shift()();
  held = null;
  await settle();
  // A read before the reply has it ticked on the Mac: still shown under the filter until the reply's read settles it
  await show([mine('review', { status: 'check', needs_you: true }), worker], 'E12');
  postHeld = [];
  const tickLate = run("move(find('boss-a', 1), 'done')");
  await show([mine('review', { status: 'check', done: true }), worker], 'E13');
  const doneRows = () => walk(lanes().find(l => l.attrs['data-k'] === 'done'), c => /^card done/.test(c.className || '')).length;
  check('need you: ticked on the Mac before the reply, still shown: ' + doneRows(), doneRows() === 1);
  postHeld.shift()();
  postHeld = null;
  await tickLate;
  await show([mine('review', { status: 'check', done: true }), worker], 'E14');
  check('need you: gone once settled', doneRows() === 0);
  run('state.filter.needs = false');

  // A finished card of yours dropped on To do sends a reopen; one on In progress is refused
  const shipped = "({ list: find('boss-a', 2).list, it: { id: 7, text: 'Ship it', column: 'done', assignee: { kind: 'you' }, done_at: 1 }, done: true })";
  await show([worker], 'F1');
  posts.length = 0;
  await run('move(' + shipped + ", 'progress')");
  check('Done → In progress: refused, nothing sent', last('refuse') === 'reopen it to To do' && !posts.length);
  await run('move(' + shipped + ", 'todo')");
  await settle();
  check('Done → To do sends a reopen: ' + JSON.stringify(posts), posts.length === 1 && posts[0][0] === '/v1/reopen' && posts[0][1] === JSON.stringify({ boss: 'boss-a', id: 7, text: 'Ship it' }));

  // The panel's rows for an item waiting on a person: who it waits on and who follows up, not On: the boss
  const rowsOf = async (boss, items, closed) => {
    lists = [{ boss, closed, items }];
    tag = 'G' + boss + items[0].id + (closed ? 'c' : '');
    es().on.board({ data: 'W/"' + tag + 'v2"' });
    await settle();
    return Object.fromEntries(walk(run("facts(find('" + boss + "', " + items[0].id + '))'), n => n.tag === 'div').map(d => [textOf(d.children[0]), textOf(d.children[1])]));
  };
  const person = (extra = {}) => ({ id: 19, text: 'Ask', status: 'waiting', status_on: 'Alicja Serafin', on: 'Alicja Serafin', waiting_for: 'Alicja Serafin', column: 'waiting',
    assignee: { kind: 'boss', name: 'boss-vitay' }, ...extra });
  const live = await rowsOf('boss-vitay', [person()], false);
  check('waiting on a person, live boss: ' + JSON.stringify(live), live.Status === 'Waiting' && live['Waiting for'] === 'Alicja Serafin' && live['Follows up'] === 'boss-vitay' && !('On' in live) && live.List === 'boss-vitay');
  const shut = await rowsOf('boss-vitay', [person({ status: 'boss closed', check: { kind: 'boss closed' } })], true);
  check('waiting on a person, closed boss: ' + JSON.stringify(shut), shut.Status === 'Waiting' && shut['Waiting for'] === 'Alicja Serafin' && shut['Follows up'] === 'no one (boss-vitay is closed)' && !('On' in shut) && !('Check' in shut) && shut.List === 'boss-vitay (closed)');
  const onWorkerRows = await rowsOf('boss-a', [{ id: 5, text: 'Fix', status: 'working', status_on: 'dev-x', on: 'dev-x', column: 'progress', session: { name: 'dev-x' }, assignee: { kind: 'worker', name: 'dev-x', running: true } }], false);
  check('on a worker: ' + JSON.stringify(onWorkerRows), onWorkerRows.Status === 'In progress · working' && onWorkerRows.On === 'dev-x' && !('Follows up' in onWorkerRows));
  const goneRows = await rowsOf('boss-a', [{ id: 7, text: 'Fix', status: 'gone', status_on: 'dev-x', on: 'dev-x', column: 'todo', session: { name: 'dev-x' }, assignee: { kind: 'boss', name: 'boss-a' } }], false);
  check('a worker gone names it: ' + JSON.stringify(goneRows), goneRows.Status === 'To do · gone · dev-x');
  const closedPlain = await rowsOf('boss-shed', [{ id: 8, text: 'Plain', status: 'boss closed', column: 'todo', assignee: { kind: 'boss', name: 'boss-shed' }, check: { kind: 'boss closed' } }], true);
  check('closed boss, no wait: ' + JSON.stringify(closedPlain), closedPlain.Status === 'To do' && closedPlain.On === 'boss-shed' && closedPlain.List === 'boss-shed (closed)' && !('Check' in closedPlain) && !('Follows up' in closedPlain));
  const closedLinked = await rowsOf('boss-shed', [{ id: 11, text: 'Linked', status: 'boss closed', status_on: 'dev-x', on: 'dev-x', column: 'todo', session: { name: 'dev-x' }, assignee: { kind: 'boss', name: 'boss-shed' } }], true);
  check('closed boss, linked to a worker, names it: ' + JSON.stringify(closedLinked), closedLinked.Status === 'To do · dev-x');
  const stillWaiting = await rowsOf('boss-vitay', [person({ status: 'blocked', status_on: 'boss-vitay' })], false);
  check('waiting on a person, status not waiting: ' + JSON.stringify(stillWaiting), stillWaiting.Status === 'Waiting · blocked' && stillWaiting['Waiting for'] === 'Alicja Serafin');
  const lateStart = await rowsOf('boss-a', [{ id: 9, text: 'Go', status: 'starting', status_on: 'boss-a', column: 'progress', start_late: true, assignee: { kind: 'boss', name: 'boss-a' } }], false);
  check('a start not picked up: ' + JSON.stringify(lateStart), lateStart.Status === 'In progress · start not picked up');
  const waitsOnWorker = await rowsOf('boss-a', [{ id: 10, text: 'Fix', status: 'waiting', status_on: 'dev-x', waiting_for: 'Carl', column: 'progress', session: { name: 'dev-x' }, assignee: { kind: 'worker', name: 'dev-x', running: true } }], false);
  check('waiting_for on a worker keeps On: ' + JSON.stringify(waitsOnWorker), waitsOnWorker.On === 'dev-x' && !('Waiting for' in waitsOnWorker));
  const mineRows = await rowsOf('boss-a', [{ id: 6, text: 'Mine', status: 'yours', column: 'todo', assignee: { kind: 'you' }, check: { kind: 'merged', pr: 12 } }], false);
  check('the user\'s own: ' + JSON.stringify(mineRows), mineRows.Status === 'To do' && mineRows.On === 'you' && mineRows.Check === 'merged · PR #12' && !('Waiting for' in mineRows));

  // A redraw between a press on a card's option and its release replaced the button: the tap took no click
  lists = [{ boss: 'boss-a', items: [{ id: 3, text: 'Pick', status: 'decide', needs_you: true, column: 'todo', options: ['a', 'b'], added: 5, assignee: { kind: 'you' } }] }];
  ctx.location.hash = '#/';
  tag = 'P';
  es().on.board({ data: 'W/"Pv2"' });
  await settle();
  const optB = () => walk(els.view, n => n.attrs?.['data-f'] === '#/item/boss-a/3/b')[0];
  const onBoard = { closest: s => s === '#view button, #view a' ? {} : null };
  const fire = (k, target = onBoard) => ctx.docOn[k].forEach(f => f({ pointerId: 1, pointerType: 'touch', button: 0, detail: 1, target }));
  const past = ms => new Promise(r => setTimeout(r, run(ms) + 100));
  const other = (id, t) => {
    lists[0].items = [lists[0].items[0], { id, text: 'other', status: 'yours', column: 'todo', assignee: { kind: 'you' } }]; // one more: To do shows a few
    tag = t;
    es().on.board({ data: 'W/"' + t + 'v2"' });
    return settle();
  };
  let pressed = optB();
  fire('pointerdown');
  await other(4, 'Q');
  check('a redraw between press and release keeps the pressed option drawn', !!pressed && optB() === pressed);
  fire('pointerup');
  await settle();
  check('a touch\'s click after the release finds the pressed option still drawn', optB() === pressed);
  fire('click');
  pressed.on.click();
  check('a redraw between press and release still picks', run("state.picks['boss-a/3']?.option") === 'b' && ctx.location.hash === '#/item/boss-a/3');
  await settle();
  check('the held redraw follows the click', optB() !== pressed && run('state.etag') === 'W/"Qv2"' && walk(els.view, n => n.attrs?.href === '#/item/boss-a/4').length === 1);
  ctx.location.hash = '#/';
  pressed = optB();
  fire('pointerdown');
  await other(8, 'U');
  fire('pointerup');
  fire('click');
  run('render()'); // a filter's or Show more's own render, in its click
  check('a click\'s own render redraws at once', optB() !== pressed && walk(els.view, n => n.attrs?.href === '#/item/boss-a/8').length === 1);
  await settle();
  pressed = optB();
  fire('pointerdown'); // never released
  await other(5, 'R');
  check('a press with no release holds the board', optB() === pressed);
  await past('HOLD_MS');
  check('… for a moment only', optB() !== pressed && walk(els.view, n => n.attrs?.href === '#/item/boss-a/5').length === 1);
  fire('pointerup');
  await past('RELEASE_MS');
  pressed = optB();
  fire('pointerdown');
  await other(6, 'S');
  fire('pointercancel'); // a swipe took the touch
  await past('RELEASE_MS');
  check('a cancelled press redraws soon', optB() !== pressed && walk(els.view, n => n.attrs?.href === '#/item/boss-a/6').length === 1);
  pressed = optB();
  fire('pointerdown', { closest: () => null }); // off the board: the header, the sheet
  await other(7, 'T');
  check('a press off the board holds nothing', optB() !== pressed && walk(els.view, n => n.attrs?.href === '#/item/boss-a/7').length === 1);
  pressed = optB();
  fire('pointerdown');
  lists[0].items[0].text = 'Pick again';
  tag = 'V';
  es().on.board({ data: 'W/"Vv2"' });
  await settle();
  fire('pointerup');
  fire('click');
  pressed.on.click();
  check('a pick from a card whose question changed during the press is dropped', !run("state.picks['boss-a/3']"));
  // The sheet in its three states: To do with details, In progress with its session's block, Done read-only
  const sheetOf = async (ls, call) => {
    lists = ls;
    tag = 'S' + Math.random();
    es().on.board({ data: 'W/"' + tag + 'v2"' });
    await settle();
    return run(call);
  };
  const cls = re => nodes => nodes.flatMap(n => walk(n, c => re.test(c.className || '')));
  const reopenBtns = nodes => nodes.flatMap(n => walk(n, c => c.tag === 'button' && c.children[0] === 'Reopen'));
  const draft = 'Draft ready, not sent\n\nHallo Frau Weber,\nanbei die Rechnung.\n\n- attach the PDF\n  - the March one\n- cc Jana';
  let s = await sheetOf([{ boss: 'boss-a', items: [{ id: 1, text: 'Send the reminder', status: 'yours', column: 'todo', assignee: { kind: 'you' }, details: draft, context: ['template: Mahnung 2'] }] }], "item('boss-a', 1)");
  const det = cls(/^details$/)(s)[0];
  check('To do: details under the title, before the context', !!det && s.indexOf(det) < s.findIndex(n => n.className === 'context') && !cls(/^live\b/)(s).length);
  check('details: the draft\'s state the first line, its own paragraph', textOf(det.children[0]) === 'Draft ready, not sent' && det.children[0].tag === 'p');
  check('details: a paragraph keeps its lines, a list its items, an indented one a sub-item', textOf(det.children[1]) === 'Hallo Frau Weber,\nanbei die Rechnung.'
    && det.children[2].tag === 'ul' && det.children[2].children.length === 3 && det.children[2].children[1].className === 'sub');
  const long = 'Sent today 12:40 to accounts@hartmann.example, Gmail\n\n' + 'Sehr geehrte Damen und Herren, '.repeat(130).trim();
  s = await sheetOf([{ boss: 'boss-a', items: [{ id: 1, text: 'Send it', status: 'yours', column: 'todo', assignee: { kind: 'you' }, details: long.slice(0, 4000) }] }], "item('boss-a', 1)");
  check('a 4000-character draft is drawn whole', textOf(cls(/^details$/)(s)[0]).replace(/\n/g, '').length === long.slice(0, 4000).replace(/\n/g, '').length);

  const fixing = { id: 5, text: 'Fix the login', status: 'working', status_on: 'dev-x', on: 'dev-x', column: 'progress', session: { id: '$2', name: 'dev-x' },
    assignee: { kind: 'worker', name: 'dev-x', running: true }, now: 'go test ./...', note: 'finished: PR #285 open, CI running', added: Math.floor(Date.now() / 1000) - 42 * 60,
    link: 'https://github.com/o/r/pull/285', check: { kind: 'checks failing', pr: 285 }, details: 'Only the token expiry.' };
  s = await sheetOf([{ boss: 'boss-a', items: [fixing] }], "item('boss-a', 5)");
  const lv = cls(/^live\b/)(s)[0], lvText = textOf(lv || {});
  check('In progress: the live block first, under the title: ' + lvText, !!lv && s.indexOf(lv) === 2 && s.indexOf(lv) < s.indexOf(cls(/^details$/)(s)[0]) && lv.className === 'live s-working');
  check('live block: state, how long, now, last note with its kind, PR and check', /dev-xworkingon it 42m/.test(lvText) && /Nowgo test/.test(lvText) && /Lastfinished: PR #285 open/.test(lvText)
    && /#285checks failing/.test(lvText) && walk(lv, c => c.tag === 'i')[0]?.children[0] === 'finished');
  check('In progress: Now left out of the facts, the block says it', !Object.keys(Object.fromEntries(walk(s.at(-1), n => n.tag === 'div').map(d => [textOf(d.children[0]), 1]))).includes('Now'));
  s = await sheetOf([{ boss: 'boss-a', items: [{ ...fixing, status: 'blocked' }] }], "item('boss-a', 5)");
  check('a worker asking you: needs you', cls(/^live\b/)(s)[0]?.className === 'live s-decide' && /needs you/.test(textOf(cls(/^live\b/)(s)[0])));
  s = await sheetOf([{ boss: 'boss-a', items: [{ id: 6, text: 'Release v0.83.0', status: 'working', status_on: 'boss-a', on: 'boss', column: 'progress', assignee: { kind: 'boss', name: 'boss-a' },
    doing: { text: 'waiting on CI for #285', at: Math.floor(Date.now() / 1000) - 60 } }] }], "item('boss-a', 6)");
  const bossLv = textOf(cls(/^live\b/)(s)[0] || {});
  check("a boss's own item: only its doing line: " + bossLv, /boss-adoingsince \d\d:\d\dNowwaiting on CI for #285$/.test(bossLv));
  s = await sheetOf([{ boss: 'boss-a', items: [{ id: 7, text: 'Mine', status: 'yours', column: 'todo', assignee: { kind: 'you' } }] }], "item('boss-a', 7)");
  check('your own item: no live block', !cls(/^live\b/)(s).length);

  const at = Math.floor(Date.now() / 1000) - 3600;
  const mailed = { id: 117, text: 'Send the March invoice reminder', on: 'vitay-mail', assignee: { kind: 'worker', name: 'vitay-mail' }, done_at: at, done_by: 'boss-vitay',
    result: 'Mail sent 12:40 to accounts@hartmann.example, Gmail id 19ab3f0c2e', details: draft, context: ['template: Mahnung 2'], link: 'https://mail.example/1' };
  const asked = { id: 21, text: 'Release now or after #285?', assignee: { kind: 'you' }, answer: 'After #285', done_at: at - 60, done_by: 'you' };
  s = await sheetOf([{ boss: 'boss-vitay', items: [], finished: [mailed, asked] }], "item('boss-vitay', 117, " + at + ')');
  const res = cls(/^live result$/)(s)[0], doneText = s.map(textOf).join('|');
  check('Done: what was done under the title, when and by whom: ' + textOf(res || {}), s.indexOf(res) === 2 && /What was done.*by boss-vitay.*Mail sent 12:40/.test(textOf(res)));
  check('Done: details and context too, Done in its head', /Draft ready, not sent/.test(doneText) && /template: Mahnung 2/.test(doneText) && /#117 ·\|?Done/.test(textOf(s[0])));
  check('Done: a worker\'s item does not reopen, said why', !reopenBtns(s).length && /only your own card reopens/.test(doneText));
  check('Done: the 24-hour limit said', /Done keeps an item 24 hours/.test(doneText) && /Ticked.*by boss-vitay/.test(doneText) && /Was onvitay-mail/.test(doneText));
  s = await sheetOf([{ boss: 'boss-vitay', items: [], finished: [mailed, asked] }], "item('boss-vitay', 21, " + (at - 60) + ')');
  check('Done: your ask shows your answer and offers Reopen', /You answeredAfter #285/.test(s.map(textOf).join('')) && reopenBtns(s).length === 1);
  check('Done: one gone from the board says so', /no longer on the board/.test(run("item('boss-vitay', 117, 5)").map(textOf).join('')));

  // A Done card opens its sheet: its link's hash routes to it, the sheet draws it, Reopen calls reopen
  media.matches = true;
  await sheetOf([{ boss: 'boss-vitay', items: [], finished: [mailed, asked] }], 'render()');
  const doneLink = drawnCards().filter(c => /\bdone\b/.test(c.className)).flatMap(c => walk(c, x => x.tag === 'a')).find(a => /\/117\//.test(a.attrs.href));
  check('a Done card links to its sheet: ' + doneLink?.attrs.href, doneLink?.attrs.href === '#/done/boss-vitay/117/' + at);
  ctx.location.hash = doneLink.attrs.href;
  check('its route is the item sheet, with when it was ticked', JSON.stringify(run('route()')) === JSON.stringify({ name: 'item', boss: 'boss-vitay', id: 117, at }));
  run('render()');
  check('the sheet opens on it', els.sheet.classList.contains('on') && /What was done/.test(textOf(els.sheet)));
  ctx.location.hash = '#/';
  run('render()');

  lists = null;
  process.exit(0);
})().catch(e => { console.log('threw: ' + e.stack); process.exit(0); });

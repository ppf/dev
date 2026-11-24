package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runModel runs cases after model.js in node; each line they print is a failed check.
func runModel(t *testing.T, cases string) {
	t.Helper()
	src, err := static.ReadFile("static/model.js")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node(t), "-e", string(src)+cases).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Errorf("%v\n%s", err, out)
	}
}

// The board's lists as /v1/board gives them for each row of the placement table (todo.Owner.place), one card each.
const rows = `
const now = 1700000000 * 1000, t = 1700000000;
const live = { boss: 'boss-dev' }, closed = { boss: 'boss-shop', closed: true };
const you = { kind: 'you' }, boss = { kind: 'boss', name: 'boss-dev', running: true }, shut = { kind: 'boss', name: 'boss-shop' };
const worker = (name, running = true) => ({ kind: 'worker', name, running });
const row = (name, list, it) => ({ name, list, it: { id: 1, text: name, added: t - 60, ...it } });
const ROWS = [
  row('0 ticked', live, { done: true, column: 'done', assignee: you }),
  row('1 started', live, { status: 'starting', started: { at: t, to: 'boss-dev' }, column: 'progress', assignee: boss }),
  row('1 start late', live, { status: 'starting', started: { at: t - 900, to: 'boss-dev' }, start_late: true, column: 'progress', assignee: boss, needs_you: true }),
  row('2a closed, PR moved', closed, { status: 'check', check: { kind: 'merged', pr: 18, since: t - 120 }, column: 'review', assignee: you, needs_you: true }),
  row('2b closed, own', closed, { status: 'boss closed', column: 'todo', assignee: you }),
  row('2c closed, on a session', closed, { status: 'boss closed', on: 'shop-fix', session: { name: 'shop-fix' }, column: 'todo', assignee: shut }),
  row('2d closed, on a person', closed, { status: 'boss closed', on: 'Carl', column: 'waiting', assignee: shut, waiting_for: 'Carl' }),
  row('3a ask', live, { status: 'decide', options: ['Yes', 'No'], column: 'todo', assignee: you, needs_you: true }),
  row('3b own, doing', live, { status: 'yours', doing: { text: 'sending the cert', at: t }, column: 'progress', assignee: you }),
  row('3c own, PR moved', live, { status: 'check', check: { kind: 'approved', pr: 243, since: t - 60 }, column: 'review', assignee: you, needs_you: true }),
  row('3d own', live, { status: 'yours', column: 'todo', assignee: you }),
  row('4a boss, PR moved', live, { status: 'check', on: 'boss', check: { kind: 'merged', pr: 7 }, column: 'review', assignee: boss }),
  row('4b boss, doing', live, { status: 'working', on: 'boss', doing: { text: 'writing notes', at: t }, column: 'progress', assignee: boss }),
  row('4c boss', live, { status: 'parked', on: 'boss', column: 'todo', assignee: boss, parked: true }),
  row('5a unread, session', live, { on: 'dev-x', session: { name: 'dev-x' }, column: 'progress', assignee: worker('dev-x', false) }),
  row('5b unread, no session', live, { on: 'Carl', column: 'waiting', assignee: { ...boss, running: false }, waiting_for: 'Carl' }),
  row('6 gone', live, { status: 'gone', on: 'dev-ios', session: { name: 'dev-ios' }, column: 'todo', assignee: boss }),
  row('7 blocked', live, { status: 'blocked', on: 'dev-pop', session: { name: 'dev-pop' }, column: 'progress', assignee: worker('dev-pop'), needs_you: true }),
  row('7 asks its boss', live, { status: 'asks', on: 'dev-ask', session: { name: 'dev-ask' }, now: 'A or B?', column: 'progress', assignee: worker('dev-ask') }),
  row('8 PR moved', live, { status: 'check', on: 'dev-y', check: { kind: 'changes requested', pr: 9 }, column: 'review', assignee: worker('dev-y') }),
  row('9 on a person', live, { status: 'waiting', on: 'Carl', column: 'waiting', assignee: boss, waiting_for: 'Carl' }),
  row('10a working', live, { status: 'working', on: 'dev-web', now: 'Edit app.js', column: 'progress', assignee: worker('dev-web') }),
  row('10b planned wait', live, { status: 'waiting', on: 'dev-api', column: 'waiting', assignee: worker('dev-api') }),
  row('10c idle, PR open', live, { status: 'idle', on: 'dev-ssl', pr_state: 'open', column: 'review', assignee: worker('dev-ssl') }),
  row('10d idle', live, { status: 'idle', on: 'dev-ssl', column: 'progress', assignee: worker('dev-ssl') }),
];
const card = name => { const r = ROWS.find(r => r.name === name); return { list: r.list, it: r.it, done: !!r.it.done }; };
const say = (name, got, want) => { if (JSON.stringify(got) !== JSON.stringify(want)) console.log(name + ': ' + JSON.stringify(got) + ', want ' + JSON.stringify(want)); };
`

// Each row of the placement table lands in its column, with its assignee and marks.
func TestModelRows(t *testing.T) {
	runModel(t, rows+`
const WANT = {
  '0 ticked': ['done', ME, null, '', [], '', false],
  '1 started': ['progress', 'boss-dev', ['starting · boss-dev', 'blue'], '', [], '', false],
  '1 start late': ['progress', 'boss-dev', ['start not picked up', 'red'], '', [], '', true],
  '2a closed, PR moved': ['review', ME, ['PR #18 merged 2m', 'green'], '', [['needs you', 'red'], ['boss closed', '']], '', true],
  '2b closed, own': ['todo', ME, null, '', [['boss closed', '']], '', false],
  '2c closed, on a session': ['todo', 'boss-shop', null, '', [['boss closed', '']], '', false],
  '2d closed, on a person': ['waiting', 'boss-shop', null, '', [['waiting for Carl · no one follows up', 'amber'], ['boss closed', '']], '', false],
  '3a ask': ['todo', ME, null, '', [['decide', 'red']], '', true],
  '3b own, doing': ['progress', ME, null, '', [], 'sending the cert', false],
  '3c own, PR moved': ['review', ME, ['PR #243 approved 1m', 'green'], '', [['needs you', 'red']], '', true],
  '3d own': ['todo', ME, null, '', [], '', false],
  '4a boss, PR moved': ['review', 'boss-dev', ['PR #7 merged', 'green'], '', [], '', false],
  '4b boss, doing': ['progress', 'boss-dev', null, '', [], 'writing notes', false],
  '4c boss': ['todo', 'boss-dev', null, '', [], '', false],
  '5a unread, session': ['progress', 'dev-x', null, '?', [], '', false],
  '5b unread, no session': ['waiting', 'boss-dev', null, '?', [['waiting for Carl', 'amber']], '', false],
  '6 gone': ['todo', 'boss-dev', ['worker dev-ios gone', 'soft-red'], '', [], '', false],
  '7 blocked': ['progress', 'dev-pop', ['blocked · needs you', 'red'], '', [], '', true],
  '7 asks its boss': ['progress', 'dev-ask', null, '', [['asks its boss', 'amber']], 'A or B?', false],
  '8 PR moved': ['review', 'dev-y', ['PR #9 changes requested', 'soft-red'], '', [], '', false],
  '9 on a person': ['waiting', 'boss-dev', null, '', [['waiting for Carl', 'amber']], '', false],
  '10a working': ['progress', 'dev-web', null, 'working', [], 'Edit app.js', false],
  '10b planned wait': ['waiting', 'dev-api', null, 'waiting', [], '', false],
  '10c idle, PR open': ['review', 'dev-ssl', null, 'idle', [], '', false],
  '10d idle': ['progress', 'dev-ssl', null, 'idle', [], '', false],
};
for (const r of ROWS) {
  const x = card(r.name), m = marks(x, now);
  say(r.name, [columnOf(x), whoOf(x), m.band, m.word, m.chips, m.line, m.red], WANT[r.name]);
  say(r.name + ': parked', m.parked, r.name === '4c boss');
}
say('every row has a want', ROWS.length, Object.keys(WANT).length);
say('unchanged 2 days on a check item: its band', marks({ list: live, it: { id: 1, status: 'check', check: { kind: 'unchanged', since: t - 3 * 86400 }, column: 'todo', assignee: you } }, now).band, ['unchanged 3d', 'amber']);
say('unchanged on another item: no band', marks({ list: live, it: { id: 1, status: 'working', check: { kind: 'unchanged', since: t - 3 * 86400 }, column: 'progress', assignee: worker('w') } }, now).band, null);
say('no column named: In progress', columnOf({ list: live, it: { id: 1, status: 'working' }, done: false }), 'progress');
say('an unknown column: In progress', columnOf({ list: live, it: { id: 1, column: 'later' }, done: false }), 'progress');
const fin = { boss: 'boss-dev', items: [{ id: 1, text: 'open #1', column: 'todo', assignee: you }],
  finished: [{ id: 1, text: 'finished #1', on: 'dev-web', assignee: worker('dev-web'), done_at: t - 30 }] };
const xs = cards({ lists: [fin] });
say('finished: a done card beside an open one of the same id', xs.map(x => [x.it.text, x.done, columnOf(x), whoOf(x)]),
  [['open #1', false, 'todo', ME], ['finished #1', true, 'done', 'dev-web']]);
say('asked in the manager now: its line', marks({ list: live, it: { id: 1, status: 'decide', asking: true, options: ['A', 'B'], column: 'todo', assignee: you } }, now).line, 'asked in manager now');
say('finished: no marks', marks(xs[1], now), { band: null, word: '', chips: [], line: '', red: false, parked: false });
say('no lists: no cards', cards({}), []);
`)
}

// The Project and Assignee filters, alone and together; what needs the user; the tally; the assignee chips' order; lanes.
func TestModelFilters(t *testing.T) {
	runModel(t, rows+`
const all = ROWS.map(r => card(r.name));
const names = f => all.filter(x => matches(x, { project: '', who: [], needs: false, ...f })).map(x => x.it.text);
say('no filter: all', names({}).length, ROWS.length);
say('project shop', names({ project: 'shop' }), ['2a closed, PR moved', '2b closed, own', '2c closed, on a session', '2d closed, on a person']);
say('Me', names({ who: [ME] }), ['0 ticked', '2a closed, PR moved', '2b closed, own', '3a ask', '3b own, doing', '3c own, PR moved', '3d own']);
say('Me and dev-ssl', names({ who: [ME, 'dev-ssl'] }).length, 9);
say('project shop and Me', names({ project: 'shop', who: [ME] }), ['2a closed, PR moved', '2b closed, own']);
say('project dev and boss-shop: none', names({ project: 'dev', who: ['boss-shop'] }), []);
say('needs you', names({ needs: true }), ['1 start late', '2a closed, PR moved', '3a ask', '3c own, PR moved', '7 blocked']);
say('needs you and Me', names({ needs: true, who: [ME] }), ['2a closed, PR moved', '3a ask', '3c own, PR moved']);
const quiet = { list: live, it: { id: 9, text: 'test', quiet: true, column: 'todo', assignee: you, needs_you: true }, done: false };
say('tally: done and test items left out', tally([...all, quiet]), { need: 5, mine: 6, open: ROWS.length - 1 });
say('tally: needs you is what the needs filter shows', tally(all).need, names({ needs: true }).length);
const opens = ws => ws.reduce((a, [, n]) => a + n, 0);
say('whos: open count, Me first, then by count, then by name', whos(all).slice(0, 5), [[ME, 6], ['boss-dev', 8], ['boss-shop', 2], ['dev-ssl', 2], ['dev-api', 1]]);
say('whos: the counts add up to the tally\'s open', opens(whos(all)), tally(all).open);
say('whos: follows the Project filter', whos(all, 'shop').filter(([, n]) => n).map(([w]) => w), [ME, 'boss-shop']);
say('whos: the Project filter keeps every chip', whos(all, 'shop').length, whos(all).length);
say('whos: Me with no card of its own', whos([card('10a working')]), [[ME, 0], ['dev-web', 1]]);
const mix = [{ list: live, it: { id: 1, assignee: worker('dev-a'), done_at: t }, done: true }, { list: live, it: { id: 2, assignee: worker('dev-b'), column: 'todo' }, done: false },
  { list: live, it: { id: 3, assignee: worker('dev-b'), quiet: true }, done: false }, { list: live, it: { id: 4, assignee: worker('dev-c'), column: 'todo' }, done: false }];
say('whos: done and test items out, a 0 chip stays and sorts last', whos(mix), [[ME, 0], ['dev-b', 1], ['dev-c', 1], ['dev-a', 0]]);
const ls = lanes(all);
say('lanes: the five columns in order', ls.map(l => l.key), ['todo', 'progress', 'waiting', 'review', 'done']);
say('To do: needs you, then mine, then sessions\'', ls[0].xs.map(x => x.it.text), ['3a ask', '2b closed, own', '3d own', '2c closed, on a session', '4c boss', '6 gone']);
say('In progress: needs you first', ls[1].xs.slice(0, 2).map(x => x.it.text), ['1 start late', '7 blocked']);
const done = lanes([{ list: live, it: { id: 1, text: 'older', done_at: t - 99 }, done: true }, { list: live, it: { id: 2, text: 'newer', done_at: t - 9 }, done: true }]);
say('Done: newest first', done[4].xs.map(x => x.it.text), ['newer', 'older']);
const ticked = lanes([{ list: live, it: { id: 1, text: 'older', done_at: t - 99 }, done: true }, { list: live, it: { id: 3, text: 'just ticked', done: true }, done: true }]);
say('Done: one ticked under 5 s ago (no done_at yet) first', ticked[4].xs.map(x => x.it.text), ['just ticked', 'older']);
say('a session named me is no user', whoOf({ list: live, it: { id: 1, assignee: worker('me') }, done: false }), 'me');
`)
}

// The sheet's Start and a drop on In progress agree, on what todo.Start takes: a To do card or a late start, no ask.
func TestStartable(t *testing.T) {
	runModel(t, rows+`
for (const runs of [true, false]) {
  const starts = ROWS.filter(r => startable(r.list, r.it, runs)).map(r => r.name);
  say('startable, manager runs ' + runs, starts, runs ? ['1 start late', '2b closed, own', '2c closed, on a session', '3d own', '4c boss', '6 gone'] : ['1 start late', '3d own', '4c boss', '6 gone']);
  for (const r of ROWS) {
    const x = card(r.name), v = verdict(x, columnOf(x), 'progress', runs);
    // Own To do cards are marked doing by a drop: Start stays a button
    if (v?.act === 'start' && !startable(r.list, r.it, runs) || startable(r.list, r.it, runs) && !doable(r.list, r.it) && columnOf(x) === 'todo' && v?.act !== 'start') {
      console.log(r.name + ': drop ' + JSON.stringify(v) + ', startable ' + startable(r.list, r.it, runs));
    }
  }
}
say('the manager\'s own list: the user\'s only', [startable({ boss: 'manager' }, { status: 'idle', on: 'boss', column: 'todo' }, true), startable({ boss: 'manager' }, { status: 'yours', column: 'todo' }, true)], [false, true]);
`)
}

// The page's model reads /v1/board as the Go side writes it (cmd's TestBoardGolden): a renamed or dropped field there
// lands a card in the wrong column, with the wrong assignee or marks.
func TestModelReadsGolden(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "testdata", "board.json"))
	if err != nil {
		t.Fatal(err)
	}
	runModel(t, `
const board = `+string(golden)+`;
const got = cards(board).map(x => [x.it.text, columnOf(x), whoOf(x), marks(x).red, x.done]);
const want = [
  ['Merge #148 now or after the release?', 'todo', ME, true, false],
  ['Read the plan', 'todo', ME, false, false],
  ["Start the phone's Done now?", 'todo', ME, true, false],
  ['Re-run CI?', 'done', ME, false, true],
  ['Send the invoice', 'waiting', 'boss-a', false, false],
  ['Fix the login', 'progress', 'a-fix', false, false],
  ['Pick the layout', 'progress', 'a-ask', false, false],
  ["Parked: the phone's Done", 'todo', 'boss-a', false, false],
  ['Ship the notes', 'done', ME, false, true],
];
if (JSON.stringify(got) !== JSON.stringify(want)) console.log('golden board: ' + JSON.stringify(got));
const sent = cards(board).find(x => x.it.text === 'Send the invoice');
if (marks(sent).chips[0]?.[0] !== 'waiting for alice') console.log('waiting_for not read: ' + JSON.stringify(marks(sent)));
if (!marks(cards(board).find(x => x.it.on === 'boss')).parked) console.log('parked not read');
if (tally(cards(board)).need !== 2) console.log('an asks card counted as needing you: ' + tally(cards(board)).need);
const parkedCard = cards(board).find(x => x.it.on === 'boss');
if (parkedCard.it.waits_on?.id !== 7 || parkedCard.it.waits_on.text !== "Start the phone's Done now?" || parkedCard.it.waits_on.options.length !== 2) console.log('waits_on not read: ' + JSON.stringify(parkedCard.it.waits_on));
if (lanes(cards(board))[4].xs.find(x => x.it.text === 'Ship the notes')?.it.done_at !== 1699999940) console.log('done_at not read');
if (cards(board).find(x => x.it.text === 'Re-run CI?').it.decided?.by !== 'boss') console.log('decided not read');
const chips = whos(cards(board)), openSum = chips.reduce((n, [, c]) => n + c, 0);
if (openSum !== tally(cards(board)).open || openSum !== 7) console.log('chip counts ' + JSON.stringify(chips) + ' do not add up to the tally: ' + tally(cards(board)).open);
`)
}

// A drop's verdict, one case per row of the move rules: the call a button makes, or why it is refused.
func TestVerdict(t *testing.T) {
	runModel(t, `
const live = { boss: 'boss-dev' }, closed = { boss: 'boss-shop', closed: true };
const you = { kind: 'you' }, boss = { kind: 'boss', name: 'boss-dev', running: true };
const c = (list, it, done = false) => ({ list, it: { id: 1, text: 'x', ...it }, done });
const yours = c(live, { status: 'yours', column: 'todo', assignee: you });
const ask = c(live, { status: 'decide', options: ['a', 'b'], column: 'todo', assignee: you, needs_you: true });
const parked = c(live, { status: 'idle', on: 'boss', column: 'todo', assignee: boss });
const shut = c(closed, { status: 'boss closed', on: 'shop-fix', session: { name: 'shop-fix' }, column: 'todo', assignee: { kind: 'boss', name: 'boss-shop' } });
const doing = c(live, { status: 'yours', doing: { text: '', at: 1 }, column: 'progress', assignee: you });
const working = c(live, { status: 'working', on: 'dev-x', session: { name: 'dev-x' }, column: 'progress', assignee: { kind: 'worker', name: 'dev-x', running: true } });
const started = c(live, { status: 'starting', started: { at: 1, to: 'boss-dev' }, column: 'progress', assignee: boss });
const review = c(live, { status: 'check', column: 'review', assignee: you, needs_you: true });
const waiting = c(live, { status: 'waiting', on: 'Carl', column: 'waiting', assignee: boss, waiting_for: 'Carl' });
const done = c(live, { column: 'done', assignee: you }, true);
const desk = c({ boss: 'local_1' }, { status: 'yours', column: 'todo', assignee: you });
const mgr = c({ boss: 'manager' }, { status: 'idle', on: 'boss', column: 'todo', assignee: { kind: 'boss', name: 'manager' } });
const v = (x, to, runs = true) => JSON.stringify(verdict(x, columnOf(x), to, runs));
const got = [
  ['1 own To do → In progress: on it', v(yours, 'progress'), "doing"],
  ['1 a boss\'s To do → In progress: Start', v(parked, 'progress'), 'start'],
  ['1 a closed boss\'s session card, manager runs: Start', v(shut, 'progress'), 'start'],
  ['1 the same, no manager', v(shut, 'progress', false), 'boss-shop is closed and no manager runs to start it'],
  ['1 an ask → In progress', v(ask, 'progress'), 'answer it: open the card'],
  ['1 the manager\'s own card', v(mgr, 'progress'), "the manager's own card: ask the manager in its session"],
  ['2 own, doing → To do: stop', v(doing, 'todo'), 'stop'],
  ['2 a session\'s card → To do', v(working, 'todo'), 'dev-x is working on it'],
  ['2 a started card → To do', v(started, 'todo'), 'boss-dev was asked to start it'],
  ['3 own To do → Done: tick', v(yours, 'done'), 'tick'],
  ['3 own Review → Done: tick', v(review, 'done'), 'tick'],
  ['3 own, doing → Done: tick', v(doing, 'done'), 'tick'],
  ['3 an ask → Done', v(ask, 'done'), 'answer it instead'],
  ['3 a session\'s card → Done', v(working, 'done'), 'boss-dev ticks it'],
  ['3 a boss\'s card → Done', v(parked, 'done'), 'boss-dev ticks it'],
  ['4 → Waiting', v(yours, 'waiting'), 'set by the session'],
  ['4 → Review', v(doing, 'review'), 'set by the PR'],
  ['4 out of Waiting', v(waiting, 'todo'), 'the session moves it out of Waiting'],
  ['4 out of Review', v(review, 'progress'), 'the PR moves it out of Review'],
  ['5 own Done → To do: reopen', v(done, 'todo'), 'reopen'],
  ['5 own Done → In progress', v(done, 'progress'), 'reopen it to To do'],
  ['5 a session\'s Done → To do', v(c(live, { on: 'dev-x', column: 'done', assignee: { kind: 'worker', name: 'dev-x' } }, true), 'todo'), 'only your own card reopens'],
  ['5 a desktop app list\'s Done', v(c({ boss: 'local_1' }, { column: 'done', assignee: you }, true), 'todo'), "a desktop app boss's card: change it in its chat"],
  ['6 the same column: nothing', v(yours, 'todo'), 'null'],
  ['a desktop app list', v(desk, 'progress'), "a desktop app boss's card: change it in its chat"],
  ['a desktop app list → Done', v(desk, 'done'), "a desktop app boss's card: change it in its chat"],
  ['2 own, in progress, not doing → To do', v(c(live, { status: 'yours', column: 'progress', assignee: you }), 'todo'), 'you are working on it'],
  ['3 own, on its boss by name → Done', v(c(live, { status: 'yours', on: 'boss-dev', column: 'todo', assignee: you }), 'done'), 'not tickable from the page'],
];
for (const [name, out, want] of got) {
  const o = JSON.parse(out), is = o === null ? 'null' : o.act || o.why;
  if (is !== want) console.log(name + ': ' + out + ', want ' + want);
  if (o?.act && !o.hint) console.log(name + ': no hint');
}
`)
}

package web

import (
	"os/exec"
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
  row('4c boss', live, { status: 'idle', on: 'boss', column: 'todo', assignee: boss }),
  row('5a unread, session', live, { on: 'dev-x', session: { name: 'dev-x' }, column: 'progress', assignee: worker('dev-x', false) }),
  row('5b unread, no session', live, { on: 'Carl', column: 'waiting', assignee: { ...boss, running: false }, waiting_for: 'Carl' }),
  row('6 gone', live, { status: 'gone', on: 'dev-ios', session: { name: 'dev-ios' }, column: 'todo', assignee: boss }),
  row('7 blocked', live, { status: 'blocked', on: 'dev-pop', session: { name: 'dev-pop' }, column: 'progress', assignee: worker('dev-pop'), needs_you: true }),
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
  '0 ticked': ['done', 'me', null, '', [], '', false],
  '1 started': ['progress', 'boss-dev', ['starting · boss-dev', 'blue'], '', [], '', false],
  '1 start late': ['progress', 'boss-dev', ['start not picked up', 'red'], '', [], '', true],
  '2a closed, PR moved': ['review', 'me', ['PR #18 merged 2m', 'green'], '', [['needs you', 'red'], ['boss closed', '']], '', true],
  '2b closed, own': ['todo', 'me', null, '', [['boss closed', '']], '', false],
  '2c closed, on a session': ['todo', 'boss-shop', null, '', [['boss closed', '']], '', false],
  '2d closed, on a person': ['waiting', 'boss-shop', null, '', [['waiting for Carl · no one follows up', 'amber'], ['boss closed', '']], '', false],
  '3a ask': ['todo', 'me', null, '', [['decide', 'red']], '', true],
  '3b own, doing': ['progress', 'me', null, '', [], 'sending the cert', false],
  '3c own, PR moved': ['review', 'me', ['PR #243 approved 1m', 'green'], '', [['needs you', 'red']], '', true],
  '3d own': ['todo', 'me', null, '', [], '', false],
  '4a boss, PR moved': ['review', 'boss-dev', ['PR #7 merged', 'green'], '', [], '', false],
  '4b boss, doing': ['progress', 'boss-dev', null, '', [], 'writing notes', false],
  '4c boss': ['todo', 'boss-dev', null, '', [], '', false],
  '5a unread, session': ['progress', 'dev-x', null, '?', [], '', false],
  '5b unread, no session': ['waiting', 'boss-dev', null, '?', [['waiting for Carl', 'amber']], '', false],
  '6 gone': ['todo', 'boss-dev', ['worker dev-ios gone', 'soft-red'], '', [], '', false],
  '7 blocked': ['progress', 'dev-pop', ['blocked · needs you', 'red'], '', [], '', true],
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
  [['open #1', false, 'todo', 'me'], ['finished #1', true, 'done', 'dev-web']]);
say('finished: no marks', marks(xs[1], now), { band: null, word: '', chips: [], line: '', red: false });
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
say('whos: Me first, then by count, then by name', whos(all).slice(0, 5), [['me', 7], ['boss-dev', 8], ['boss-shop', 2], ['dev-ssl', 2], ['dev-api', 1]]);
say('whos: Me with no card of its own', whos([card('10a working')]), [['me', 0], ['dev-web', 1]]);
const ls = lanes(all);
say('lanes: the five columns in order', ls.map(l => l.key), ['todo', 'progress', 'waiting', 'review', 'done']);
say('To do: needs you, then mine, then sessions\'', ls[0].xs.map(x => x.it.text), ['3a ask', '2b closed, own', '3d own', '2c closed, on a session', '4c boss', '6 gone']);
say('In progress: needs you first', ls[1].xs.slice(0, 2).map(x => x.it.text), ['1 start late', '7 blocked']);
const done = lanes([{ list: live, it: { id: 1, text: 'older', done_at: t - 99 }, done: true }, { list: live, it: { id: 2, text: 'newer', done_at: t - 9 }, done: true }]);
say('Done: newest first', done[4].xs.map(x => x.it.text), ['newer', 'older']);
`)
}

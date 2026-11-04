import type { On } from 'claude-code'
import { expect, mock, test } from 'claude-code/testing'
import type { Engine } from 'claude-code/testing'

import type { Worker } from '../types'

const PANE = {
  plugin: 'dev-board',
  component: 'Pane',
  requestId: 'board',
  props: {
    title: 'Workers',
    isFocused: false,
    bodyColumns: 60,
    placement: 'dock',
    scroll: { offset: 0, bodyRows: 40 },
    view: {},
  },
} as const

const BAND = {
  plugin: 'dev-board',
  component: 'AbovePrompt',
  props: { hasSurvey: false, isWorking: false, maxRows: 3, bodyColumns: 80, scroll: { offset: 0, bodyRows: 3 }, view: {} },
} as const

const worker = (fields: Partial<Worker>): Worker => ({
  id: 'local_a',
  name: 'a',
  title: 'Worker A',
  branch: 'feature/a',
  worktree: '/wt/a',
  prs: [],
  state: 'idle',
  createdAt: 1,
  lastActivityAt: 1,
  verdict: '',
  detail: '',
  needsAction: '',
  lastLines: [],
  tool: '',
  context: null,
  notes: [],
  status: 'idle',
  statusText: '',
  since: '',
  plan: [],
  ...fields,
})

const WORKERS = [
  worker({ state: 'busy', status: 'working', statusText: 'Bash', tool: 'Bash', context: 42, lastLines: ['Running the tests.'], verdict: 'completed' }),
  worker({
    id: 'local_b',
    title: 'Worker B',
    state: 'blocked',
    status: 'blocked',
    statusText: 'pick one',
    verdict: 'blocked',
    detail: 'two ways',
    needsAction: 'pick one',
    prs: [{ number: 78, url: 'https://github.com/o/r/pull/78', state: 'OPEN' }],
  }),
  worker({ id: 'local_c', title: 'Worker C', state: 'stopped', status: 'stopped' }),
]

type Ran = { exitCode: number; stdout: string; stderr: string }

const STUCK = { key: 'local_a/stuck/100', kind: 'stuck', text: 'no progress for 17m' }
const RESUME = { key: 'local_b/resume due/200', kind: 'resume due', text: 'rate limit reset 18:05: resume it' }

/** What the other commands the mod runs answer: gh pr checks, dev delivered, open, pbcopy (by argv[0]/argv[1]). */
type Others = Partial<Record<'gh' | 'delivered' | 'open' | 'pbcopy', Ran | string>>

/** Every run of a command other than `dev desktop board`, with its stdin. */
type OtherRun = { argv: readonly string[]; stdin?: string }

/**
 * Starts the session with `dev desktop board` answering `answer()`'s run, or failing to start with that reason;
 * `others` answers the rest (gh's checks none, dev delivered exits 1, open and pbcopy pass by default).
 */
async function start($: Engine, on: On, ran: Ran | string | (() => Ran | string), others: Others = {}) {
  const answer = typeof ran === 'function' ? ran : () => ran
  const runs: (readonly string[])[] = []
  const otherRuns: OtherRun[] = []
  on('command.register', () => ({ value: { command: 'board' } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('process.run', (_, e) => {
    const isBoard = e.argv[1] === 'desktop'
    const now = isBoard
      ? answer()
      : (others[e.argv[1] === 'delivered' ? 'delivered' : (e.argv[0] as 'gh' | 'open' | 'pbcopy')] ??
        (e.argv[0] === 'gh' ? { exitCode: 1, stdout: '', stderr: 'no checks reported' } : e.argv[1] === 'delivered' ? { exitCode: 1, stdout: '', stderr: 'not delivered' } : { exitCode: 0, stdout: '', stderr: '' }))
    if (isBoard) {
      runs.push(e.argv)
    } else {
      otherRuns.push({ argv: e.argv, stdin: e.init?.stdin })
    }
    if (typeof now === 'string') {
      return { deny: now }
    }
    return { value: { ...now, isStdoutTruncated: false, isStderrTruncated: false } }
  })
  mock.env(on, { CLAUDE_CODE_HOST_SESSION_ID: 'local_boss' })
  const clock = mock.clock(on)
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await clock.settle()
  return { runs, otherRuns, clock }
}

const ok = (workers: Worker[], stderr = ''): Ran => ({ exitCode: 0, stdout: JSON.stringify(workers), stderr })

test('the pane shows a tile per worker, on every surface', async ($, on) => {
  const { runs, clock } = await start($, on, ok(WORKERS))
  await clock.advance(2000)
  expect(runs[0]).toEqual(['dev', 'desktop', 'board', '--json', '--boss', 'local_boss'])
  expect(runs.length).toBe(2)

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect((await ui.find({ type: 'Text', text: /^busy · feature\/a · context 42%$/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /^working/ }))?.text).toBe('working · Bash')
    expect((await ui.find({ type: 'Text', text: /Running the tests/ }))?.text).toBe('Running the tests.')
    expect((await ui.find({ type: 'Text', text: /last turn: completed/ }))?.text).toBe('last turn: completed')
    expect((await ui.find({ type: 'Text', text: /^blocked ·/ }))?.text).toBe('blocked · pick one')
    expect((await ui.find({ type: 'Text', text: /^blocked: two ways$/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /PR #78 open/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /Worker C/ }))?.text).toBeTruthy()
    await ui.unmount()
  }
})

test('the band sums the workers up', async ($, on) => {
  await start($, on, ok(WORKERS))
  const ui = await $.ui.mount({ ...BAND, surface: 'desktop' })
  expect((await ui.find({ type: 'Text' }))?.text).toBe('3 workers · 1 busy · 1 blocked · PR #78 open')
  await ui.unmount()
})

test('the band is hidden with no workers', async ($, on) => {
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)

    return <Text>engine</Text>
  })
  await start($, on, ok([]))
  const ui = await $.ui.mount({ ...BAND, surface: 'desktop' })
  expect((await ui.find({ type: 'Text' }))?.text).toBe('engine')
  await ui.unmount()

  const pane = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await pane.find({ type: 'Text' }))?.text).toBe('No workers.')
  await pane.unmount()
})

test('a failed run shows its error in the pane', async ($, on) => {
  await start($, on, { exitCode: 1, stdout: '', stderr: '❌ no app session record for local_boss' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text' }))?.text).toBe('❌ no app session record for local_boss')
  await ui.unmount()
})

test('a dev that cannot start shows why', async ($, on) => {
  await start($, on, 'spawn dev ENOENT')
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /ENOENT/ }))?.text).toMatch(/^dev desktop board: .*ENOENT/)
  await ui.unmount()
})

test('warnings show above the tiles', async ($, on) => {
  await start($, on, ok(WORKERS, '⚠ dev desktop board: x.json: unexpected end of JSON input'))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /x\.json/ }))?.text).toMatch(/unexpected end/)
  expect((await ui.find({ type: 'Text', text: /Worker B/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('outside the desktop app it says so and runs nothing', async ($, on) => {
  let runs = 0
  on('command.register', () => ({ value: { command: 'board' } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('process.run', () => {
    runs += 1
    return { value: { exitCode: 0, stdout: '[]', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  mock.env(on, {})
  const clock = mock.clock(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await clock.advance(10_000)
  expect(runs).toBe(0)
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect((await ui.find({ type: 'Text' }))?.text).toMatch(/Not a desktop app session/)
  await ui.unmount()
})

test('each poll that changes the board redraws it', async ($, on) => {
  let now: Ran = ok(WORKERS)
  const { clock } = await start($, on, () => now)
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /Worker B/ }))?.text).toBeTruthy()

  now = { exitCode: 1, stdout: '', stderr: 'boom' }
  await clock.advance(2000)
  expect((await ui.find({ type: 'Text' }))?.text).toBe('boom')

  now = ok(WORKERS)
  await clock.advance(2000)
  expect(await ui.find({ type: 'Text', text: /^boom$/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /Worker B/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test("a busy worker shows its last turn's verdict as the last turn's", async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', verdict: 'blocked', needsAction: 'old ask' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ type: 'Text', text: /old ask/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^last turn: blocked$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('output this mod does not read says so', async ($, on) => {
  await start($, on, { exitCode: 0, stdout: JSON.stringify([{ id: 'x' }]), stderr: '' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text' }))?.text).toMatch(/is dev up to date/)
  await ui.unmount()
})

test('notes show on their tiles', async ($, on) => {
  await start($, on, ok([worker({ name: 'fix-login', state: 'busy', notes: [STUCK] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^stuck:/ }))?.text).toBe('stuck: no progress for 17m')
  await ui.unmount()
})

/** Collects the prompts the mod submits; `answer` is what the engine makes of each. */
function submits(on: Parameters<typeof start>[1], answer: (text: string) => { text: string } | { drop: string } = text => ({ text })) {
  const texts: string[] = []
  on('prompt.submit', (_, e) => {
    texts.push(e.text)
    return answer(e.text)
  })
  return texts
}

test('new notes go to the boss once, batched in one prompt', async ($, on) => {
  const texts = submits(on)
  let workers = [worker({ name: 'fix-login', state: 'busy', notes: [STUCK] }), worker({ id: 'local_b', name: 'docs-api', notes: [RESUME] })]
  const { clock } = await start($, on, () => ok(workers))
  expect(texts).toEqual(['dev-board: fix-login stuck: no progress for 17m; docs-api resume due: rate limit reset 18:05: resume it'])

  // The same keys, newer minutes: not again
  workers = [worker({ name: 'fix-login', state: 'busy', notes: [{ ...STUCK, text: 'no progress for 19m' }] }), worker({ id: 'local_b', name: 'docs-api', notes: [RESUME] })]
  await clock.advance(6000)
  expect(texts.length).toBe(1)

  workers = [worker({ name: 'fix-login', state: 'busy', notes: [STUCK, { key: 'local_a/high context/c', kind: 'high context', text: '85% used' }] })]
  await clock.advance(2000)
  expect(texts).toEqual([texts[0], 'dev-board: fix-login high context: 85% used'])
})

test('a reload sends no note again', async ($, on) => {
  const texts = submits(on)
  const { runs, clock } = await start($, on, ok([worker({ name: 'fix-login', state: 'busy', notes: [STUCK] })]))
  expect(texts.length).toBe(1)
  const polled = runs.length
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await clock.settle()
  expect(runs.length).toBe(polled + 1)
  expect(texts.length).toBe(1)
})

test('a failed poll sends nothing and forgets nothing', async ($, on) => {
  const texts = submits(on)
  let now: Ran = ok([worker({ notes: [STUCK] })])
  const { clock } = await start($, on, () => now)
  now = { exitCode: 1, stdout: '', stderr: 'boom' }
  await clock.advance(2000)
  now = ok([worker({ notes: [STUCK] })])
  await clock.advance(2000)
  expect(texts.length).toBe(1)
})

test('a refused prompt is shown, not retried', async ($, on) => {
  const texts = submits(on, () => ({ drop: 'blocked by a hook' }))
  const toasts: string[] = []
  on('ui.toast', (_, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  const { clock } = await start($, on, ok([worker({ notes: [STUCK] })]))
  await clock.advance(4000)
  expect(texts.length).toBe(1)
  expect(toasts).toEqual(['dev-board: worker notes not sent: blocked by a hook'])
})

test('a dev without notes says so and sends nothing', async ($, on) => {
  const texts = submits(on)
  const { notes: _, ...old } = worker({})
  await start($, on, { exitCode: 0, stdout: JSON.stringify([old]), stderr: '' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text' }))?.text).toMatch(/is dev up to date/)
  await ui.unmount()
  expect(texts.length).toBe(0)
})

test('a dev without plans or statuses says so', async ($, on) => {
  const { plan: _, status: __, ...old } = worker({})
  await start($, on, { exitCode: 0, stdout: JSON.stringify([old]), stderr: '' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text' }))?.text).toMatch(/is dev up to date/)
  await ui.unmount()
})

const PLAN = [
  { content: 'Read the code', status: 'completed' },
  { content: 'Write the mod', status: 'in_progress' },
  { content: 'Open the PR', status: 'pending' },
] as const

test('the task list is collapsed, its count in the header; Tasks expands it', async ($, on) => {
  await start($, on, ok([worker({ plan: [...PLAN] })]))
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect((await ui.find({ type: 'Text', text: /Worker A/ }))?.text).toBe('● Worker A 1/3')
    expect(await ui.find({ type: 'Text', text: /Write the mod/ })).toBeFalsy()
    await ui.press({ key: 'local_a:tasks' })
    expect((await ui.find({ type: 'Text', text: /Read the code/ }))?.text).toBe('✅ Read the code')
    expect((await ui.find({ type: 'Text', text: /Write the mod/ }))?.text).toBe('🔄 Write the mod')
    expect((await ui.find({ type: 'Text', text: /Open the PR/ }))?.text).toBe('◌ Open the PR')
    await ui.press({ key: 'local_a:tasks' })
    expect(await ui.find({ type: 'Text', text: /Write the mod/ })).toBeFalsy()
    await ui.unmount()
  }
})

test('no task list: no count and no Tasks button', async ($, on) => {
  await start($, on, ok([worker({})]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /Worker A/ }))?.text).toBe('● Worker A')
  expect(await ui.find({ key: 'local_a:tasks' })).toBeFalsy()
  await ui.unmount()
})

test('needs you sorts first, says what it asks, and the band counts it', async ($, on) => {
  await start($, on, ok([
    worker({ state: 'busy', status: 'working', since: '12m' }),
    worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'needs you', statusText: 'allow Bash: rm -rf bin', since: '5m' }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const titles = (await ui.findAll({ type: 'Text', text: /^● / })).map(t => t.text)
  expect(titles).toEqual(['● Worker B', '● Worker A'])
  expect((await ui.find({ type: 'Text', text: /^needs you/ }))?.text).toBe('needs you · allow Bash: rm -rf bin')
  expect((await ui.find({ type: 'Text', text: /^busy 12m/ }))?.text).toBe('busy 12m · feature/a')
  await ui.unmount()
  const band = await $.ui.mount({ ...BAND, surface: 'desktop' })
  expect((await band.find({ type: 'Text' }))?.text).toBe('1 needs you · 2 workers · 2 busy')
  await band.unmount()
})

test('a waiting worker says on what and for how long', async ($, on) => {
  await start($, on, ok([worker({ status: 'waiting', statusText: 'Auto-fix: PR #80', since: '4m' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^waiting/ }))?.text).toBe('waiting 4m · Auto-fix: PR #80')
  expect((await ui.find({ type: 'Text', text: /^idle 4m ago/ }))?.text).toBe('idle 4m ago · feature/a')
  await ui.unmount()
})

const OPEN_PR = { number: 80, url: 'https://github.com/o/r/pull/80', state: 'OPEN' }
const CHECKS: Ran = {
  exitCode: 8,
  stdout: JSON.stringify([{ name: 'test', bucket: 'pass' }, { name: 'lint', bucket: 'fail' }, { name: 'e2e', bucket: 'pending' }]),
  stderr: '',
}

test("an open PR's checks show on its row, read once a minute", async ($, on) => {
  const { otherRuns, clock } = await start($, on, ok([worker({ state: 'busy', status: 'working', prs: [OPEN_PR] })]), { gh: CHECKS })
  const gh = () => otherRuns.filter(r => r.argv[0] === 'gh')
  expect(gh().map(r => r.argv)).toEqual([['gh', 'pr', 'checks', OPEN_PR.url, '--json', 'name,bucket']])
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /PR #80/ }))?.text).toBe('PR #80 open  ✅ test ❌ lint ⏳ e2e')
  await ui.unmount()
  await clock.advance(58_000)
  expect(gh().length).toBe(1)
  await clock.advance(2000)
  expect(gh().length).toBe(2)
})

test('checks that could not be read say why', async ($, on) => {
  await start($, on, ok([worker({ prs: [OPEN_PR] })]), { gh: 'spawn gh ENOENT' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /PR #80/ }))?.text).toMatch(/^PR #80 open {2}gh pr checks: .*ENOENT/)
  await ui.unmount()
})

test('a merged PR reads no checks', async ($, on) => {
  const { otherRuns } = await start($, on, ok([worker({ state: 'busy', prs: [{ ...OPEN_PR, state: 'MERGED' }] })]), { gh: CHECKS })
  expect(otherRuns.filter(r => r.argv[0] === 'gh').length).toBe(0)
})

test('Archive shows once dev delivered passes, and asks the boss to archive', async ($, on) => {
  const texts = submits(on)
  const { otherRuns } = await start($, on, ok([worker({ prs: [OPEN_PR] })]), { delivered: { exitCode: 0, stdout: 'delivered', stderr: '' } })
  expect(otherRuns.filter(r => r.argv[1] === 'delivered').map(r => r.argv)).toEqual([['dev', 'delivered', '/wt/a']])
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:archive' })
  expect(texts).toEqual(['dev-board: archive a (local_a): dev delivered exited 0'])
  await ui.unmount()
})

test('no Archive while not delivered, nor dev delivered run for a busy worker or one with no PR', async ($, on) => {
  const { otherRuns } = await start($, on, ok([
    worker({ prs: [OPEN_PR] }),
    worker({ id: 'local_b', state: 'busy', prs: [OPEN_PR] }),
    worker({ id: 'local_c' }),
  ]))
  expect(otherRuns.filter(r => r.argv[1] === 'delivered').map(r => r.argv)).toEqual([['dev', 'delivered', '/wt/a']])
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ key: 'local_a:archive' })).toBeFalsy()
  await ui.unmount()
})

test('Nudge asks the boss to relay "Continue where you left off"', async ($, on) => {
  const texts = submits(on)
  const toasts = collectToasts(on)
  await start($, on, ok([worker({})]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:nudge' })
  expect(texts).toEqual(['dev-board: nudge a (local_a): Continue where you left off'])
  expect(toasts).toEqual(['dev-board: nudge for a queued for the boss'])
  await ui.unmount()
})

test('Message… opens a field whose text the boss is asked to relay', async ($, on) => {
  const texts = submits(on)
  await start($, on, ok([worker({})]))
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
    await ui.press({ key: 'local_a:message' })
    expect(await ui.find({ key: 'local_a:draft' })).toBeTruthy()
    await ui.input({ key: 'local_a:draft', text: ' add tests ' })
    expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
    await ui.unmount()
  }
  expect(texts).toEqual(['dev-board: message a (local_a): add tests', 'dev-board: message a (local_a): add tests'])
})

test('an empty message closes the field and sends nothing', async ($, on) => {
  const texts = submits(on)
  await start($, on, ok([worker({})]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:message' })
  await ui.input({ key: 'local_a:draft', text: '  ' })
  expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
  expect(texts).toEqual([])
  await ui.unmount()
})

test('the mobile app, with no field, has no Message…', async ($, on) => {
  await start($, on, ok([worker({})]))
  const ui = await $.ui.mount({ ...PANE, surface: 'mobile' })
  expect(await ui.find({ key: 'local_a:message' })).toBeFalsy()
  expect(await ui.find({ key: 'local_a:nudge' })).toBeTruthy()
  await ui.unmount()
})

test("Copy id copies on the surface, else with the Mac's pbcopy", async ($, on) => {
  const toasts = collectToasts(on)
  let copied: boolean = true
  on('ui.copy', () => ({ value: copied ? { isCopied: true as const } : { isCopied: false as const, reason: 'no-clipboard' as const } }))
  const { otherRuns } = await start($, on, ok([worker({})]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:copy' })
  expect(otherRuns.filter(r => r.argv[0] === 'pbcopy')).toEqual([])
  copied = false
  await ui.press({ key: 'local_a:copy' })
  expect(otherRuns.filter(r => r.argv[0] === 'pbcopy')).toEqual([{ argv: ['pbcopy'], stdin: 'local_a' }])
  expect(toasts).toEqual(['Copied local_a', 'Copied local_a'])
  await ui.unmount()
})

test("Open opens the worker's session through the app's deep link", async ($, on) => {
  const toasts = collectToasts(on)
  const { otherRuns } = await start($, on, ok([worker({})]), { open: { exitCode: 1, stdout: '', stderr: 'no handler' } })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:open' })
  expect(otherRuns.filter(r => r.argv[0] === 'open').map(r => r.argv)).toEqual([['open', 'claude://code/continue?session=local_a']])
  expect(toasts).toEqual(['dev-board: could not open a: no handler'])
  await ui.unmount()
})

function collectToasts(on: On) {
  const toasts: string[] = []
  on('ui.toast', (_, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  return toasts
}

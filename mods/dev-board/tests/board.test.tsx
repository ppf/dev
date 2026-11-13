import type { On, UiPane } from 'claude-code'
import { expect, mock, test } from 'claude-code/testing'
import type { Engine } from 'claude-code/testing'

import type { TodoItem, Worker } from '../types'

// No node types in this mod's tsconfig
declare const setTimeout: (fn: (...args: any[]) => void, ms: number) => unknown

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
  props: { hasSurvey: false, isCollapsed: false, isWorking: false, maxRows: 3, bodyColumns: 80, scroll: { offset: 0, bodyRows: 3 }, view: {} },
} as const

const worker = (fields: Partial<Worker>): Worker => ({
  id: 'local_a',
  name: '',
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

/** What the other commands the mod runs answer: gh pr checks, dev delivered, dev desktop answer, dev todo, open, mv (by argv[0]/argv[1]/argv[2]). */
type Others = Partial<Record<'gh' | 'delivered' | 'answer' | 'todo' | 'open' | 'mv', Ran | string | (() => Ran | string)>>

// What a dev older than `dev todo` says to it
const NO_TODO: Ran = { exitCode: 1, stdout: '', stderr: '❌ unknown flag: --json' }

/** Every run of a command other than `dev desktop board`, with its stdin. */
type OtherRun = { argv: readonly string[]; stdin?: string }

/**
 * Starts the session with `dev desktop board` answering `answer()`'s run, or failing to start with that reason;
 * `others` answers the rest (gh's checks none, dev delivered exits 1, dev todo is unknown, open passes by default); `env` adds to its environment;
 * `panes` lists the mod's open panes (none by default).
 */
async function start($: Engine, on: On, ran: Ran | string | (() => Ran | string), others: Others = {}, env: Record<string, string> = {}, panes: () => UiPane[] = () => []) {
  const answer = typeof ran === 'function' ? ran : () => ran
  const runs: (readonly string[])[] = []
  const otherRuns: OtherRun[] = []
  const todoRuns: (readonly string[])[] = []
  on('command.register', () => ({ value: { command: 'board' } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('ui.panes', () => ({ value: panes() }))
  on('process.run', (_, e) => {
    const isBoard = e.argv[1] === 'desktop' && e.argv[2] === 'board'
    const isTodo = e.argv[1] === 'todo'
    const other = others[e.argv[1] === 'delivered' || isTodo ? e.argv[1] as 'delivered' | 'todo' : e.argv[2] === 'answer' ? 'answer' : (e.argv[0] as 'gh' | 'open' | 'mv')]
    const now = isBoard
      ? answer()
      : ((typeof other === 'function' ? other() : other) ??
        (e.argv[0] === 'gh' ? { exitCode: 1, stdout: '', stderr: 'no checks reported' } : e.argv[1] === 'delivered' ? { exitCode: 1, stdout: '', stderr: 'not delivered' } : isTodo ? NO_TODO : { exitCode: 0, stdout: '', stderr: '' }))
    if (isBoard) {
      runs.push(e.argv)
    } else if (isTodo) {
      todoRuns.push(e.argv)
    } else {
      otherRuns.push({ argv: e.argv, stdin: e.init?.stdin })
    }
    if (typeof now === 'string') {
      return { deny: now }
    }
    return { value: { ...now, isStdoutTruncated: false, isStderrTruncated: false } }
  })
  mock.env(on, { CLAUDE_CODE_HOST_SESSION_ID: 'local_boss', ...env })
  const clock = mock.clock(on)
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await clock.settle()
  return { runs, otherRuns, todoRuns, clock }
}

const ok = (workers: Worker[], stderr = ''): Ran => ({ exitCode: 0, stdout: JSON.stringify(workers), stderr })

type Mounted = Pick<Awaited<ReturnType<Engine['ui']['mount']>>, 'find' | 'findAll' | 'press'>

/** A drawn element's children: strings and elements, as `FoundElement.children` holds them. */
type Drawn = string | { type: string; children?: Drawn[] }

const shown = (el: Drawn): string => (typeof el === 'string' ? el : (el.children ?? []).map(shown).join(''))
const texts = (el: Drawn): string[] => (typeof el === 'string' ? [] : el.type === 'Text' ? [shown(el)] : (el.children ?? []).flatMap(texts))

/** A keyed element's Texts as read, in order. */
async function textsOf(ui: Mounted, key: string) {
  const el = await ui.find({ key })
  return el === undefined ? undefined : (el.children as Drawn[]).flatMap(texts)
}

/** A row's first line as read: its Texts (the name, what it says, its time), split by a space. */
async function header(ui: Mounted, id = 'local_a') {
  return (await textsOf(ui, `${id}:header`))?.join(' ')
}

/** Opens a worker's detail by its name, opening the idle line first when the worker sits in it. */
async function unfold(ui: Mounted, id = 'local_a') {
  if ((await ui.find({ key: `${id}:fold` })) === undefined) await ui.press({ key: 'idle:fold' })
  if ((await ui.find({ key: `${id}:detail` })) === undefined) await ui.press({ key: `${id}:fold` })
}

test('the pane shows a row per worker, on every surface', async ($, on) => {
  const { runs, clock } = await start($, on, ok(WORKERS))
  await clock.advance(2000)
  expect(runs[0]).toEqual(['dev', 'desktop', 'board', '--json', '--boss', 'local_boss'])
  expect(runs.length).toBe(2)

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    // Its status left out: the Working section says it
    expect(await header(ui)).toBe('Worker A Bash')
    expect(await ui.find({ type: 'Text', text: /^working/ })).toBeFalsy()
    expect((await ui.find({ type: 'Text', text: /^42%$/ }))?.text).toBeTruthy()
    // The branch and reply lines wait in the detail; the verdict has no row
    expect(await ui.find({ type: 'Text', text: /^feature\/a$/ })).toBeFalsy()
    expect(await ui.find({ type: 'Text', text: /Running the tests|last turn/ })).toBeFalsy()
    await unfold(ui)
    expect((await ui.find({ type: 'Text', text: /^feature\/a$/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /Running the tests/ }))?.text).toBe('Running the tests.')
    // The status said once, its need and the verdict's detail not again
    expect(await header(ui, 'local_b')).toBe('Worker B blocked · pick one')
    expect((await ui.findAll({ type: 'Text', text: /blocked|pick one|two ways/ })).map(t => t.text)).toEqual(['blocked · pick one'])
    expect(await ui.find({ key: 'local_b:https://github.com/o/r/pull/78' })).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /Worker C/ }))?.text).toBe('Worker C')
    await ui.press({ key: 'local_a:fold' })
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
  expect(await textsOf(pane, 'empty')).toEqual(['○', 'No workers yet', 'start_session from this boss session adds one'])
  await pane.unmount()
})

test('a failed run shows its error in the pane', async ($, on) => {
  await start($, on, { exitCode: 1, stdout: '', stderr: '❌ no app session record for local_boss' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await textsOf(ui, 'error')).toEqual(['⚠', '❌ no app session record for local_boss', 'retries every 2 s'])
  await ui.unmount()
})

test('a dev that cannot start shows why', async ($, on) => {
  await start($, on, 'spawn dev ENOENT')
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /ENOENT/ }))?.text).toMatch(/^dev desktop board: .*ENOENT/)
  await ui.unmount()
})

test('warnings show above the rows, a row each: ⚠, then its line without dev\'s prefix', async ($, on) => {
  await start($, on, ok(WORKERS, '⚠ dev desktop board: x.json: unexpected end of JSON input\n⚠ dev desktop board: dev-board mod changed 19:17, after this session started 17:27: start a new session to load it'))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await textsOf(ui, 'warnings')).toEqual([
    '⚠',
    'x.json: unexpected end of JSON input',
    '⚠',
    'dev-board mod changed 19:17, after this session started 17:27: start a new session to load it',
  ])
  expect((await ui.find({ type: 'Text', text: /Worker B/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test("a warning line without dev's prefix shows as it is; blank lines show none", async ($, on) => {
  await start($, on, ok(WORKERS, 'plain line\n\n⚠ bare'))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await textsOf(ui, 'warnings')).toEqual(['⚠', 'plain line', '⚠', 'bare'])
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
  // Nothing polls: no retry to promise
  expect(await textsOf(ui, 'error')).toEqual(['⚠', 'Not a desktop app session: CLAUDE_CODE_HOST_SESSION_ID is unset.'])
  await ui.unmount()
})

const BOSS_PROMPT = "dev-board: /board opened — load the dev-board:boss skill and act as boss for this session's workers."

/** Runs `/board` as the person typing it would. */
const runBoard = ($: Engine) =>
  $.command.run({ command: 'board', args: '', origin: { kind: 'composer' }, presentation: { isFullscreen: false, columns: 80 } })

test('/board asks the session to be the boss once: not again on reopening the pane or a reload', async ($, on) => {
  const texts = submits(on)
  const toasts = collectToasts(on)
  on('ui.open', () => ({ value: { isPlaced: true } }))
  const { clock } = await start($, on, ok([]))
  expect(texts).toEqual([])

  expect(await runBoard($)).toEqual({ text: 'Worker board opened.' })
  // Again before the prompt's timer fires
  await runBoard($)
  await clock.settle()
  expect(texts).toEqual([BOSS_PROMPT])
  expect(toasts).toEqual(['boss mode queued for the boss'])

  await runBoard($)
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await runBoard($)
  await clock.settle()
  expect(texts).toEqual([BOSS_PROMPT])
})

test('a refused boss prompt is shown, not retried', async ($, on) => {
  const texts = submits(on, () => ({ drop: 'blocked by a hook' }))
  const toasts = collectToasts(on)
  on('ui.open', () => ({ value: { isPlaced: true } }))
  const { clock } = await start($, on, ok([]))
  await runBoard($)
  await clock.settle()
  await runBoard($)
  await clock.settle()
  expect(texts).toEqual([BOSS_PROMPT])
  expect(toasts).toEqual(['boss mode not sent: blocked by a hook'])
})

test('a session whose stored state says it was asked is not asked again', async ($, on) => {
  // What the mod stored before a reload, which starts its module variables over; held here so writes go through
  let stored = { value: { shape: 'boss-asked-1', value: true } as unknown, version: 1 }
  on('state.get', (_, e, next) => (e.key === 'bossAsked' ? { value: stored } : next(e)))
  on('state.set', (_, e, next) => {
    if (e.key !== 'bossAsked') return next(e)
    if (e.ifVersion !== undefined && e.ifVersion !== stored.version) return { value: { isSet: false, version: stored.version } }
    stored = { value: e.value, version: stored.version + 1 }
    return { value: { isSet: true, version: stored.version } }
  })
  const texts = submits(on)
  on('ui.open', () => ({ value: { isPlaced: true } }))
  const { clock } = await start($, on, ok([]))
  await runBoard($)
  await clock.settle()
  expect(texts).toEqual([])
})

test('outside the desktop app /board asks no one to be the boss', async ($, on) => {
  const texts = submits(on)
  on('command.register', () => ({ value: { command: 'board' } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('ui.open', () => ({ value: { isPlaced: false, reason: 'no panes here' } }))
  mock.env(on, {})
  const clock = mock.clock(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(await runBoard($)).toEqual({ text: 'Worker board opened; this surface does not place panes.' })
  await clock.settle()
  expect(texts).toEqual([])
})

test('each poll that changes the board redraws it', async ($, on) => {
  let now: Ran = ok(WORKERS)
  const { clock } = await start($, on, () => now)
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /Worker B/ }))?.text).toBeTruthy()

  now = { exitCode: 1, stdout: '', stderr: 'boom' }
  await clock.advance(2000)
  expect((await textsOf(ui, 'error'))?.[1]).toBe('boom')

  now = ok(WORKERS)
  await clock.advance(2000)
  expect(await ui.find({ type: 'Text', text: /^boom$/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /Worker B/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test("a busy worker's last verdict and need show nowhere on its row", async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', verdict: 'blocked', needsAction: 'old ask' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  expect(await ui.find({ type: 'Text', text: /old ask|last turn/ })).toBeFalsy()
  expect(await header(ui)).toBe('Worker A working')
  await ui.unmount()
})

test('output this mod does not read says so', async ($, on) => {
  await start($, on, { exitCode: 0, stdout: JSON.stringify([{ id: 'x' }]), stderr: '' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await textsOf(ui, 'error'))?.[1]).toMatch(/is dev up to date/)
  await ui.unmount()
})

test('notes show under their row, each a line with ⚠', async ($, on) => {
  await start($, on, ok([worker({ name: 'fix-login', state: 'busy', notes: [STUCK] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^⚠ stuck:/ }))?.text).toBe('⚠ stuck: no progress for 17m')
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
  expect(toasts).toEqual(['worker notes not sent: blocked by a hook'])
})

test('a dev without notes says so and sends nothing', async ($, on) => {
  const texts = submits(on)
  const { notes: _, ...old } = worker({})
  await start($, on, { exitCode: 0, stdout: JSON.stringify([old]), stderr: '' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await textsOf(ui, 'error'))?.[1]).toMatch(/is dev up to date/)
  await ui.unmount()
  expect(texts.length).toBe(0)
})

test('a dev without plans or statuses says so', async ($, on) => {
  const { plan: _, status: __, ...old } = worker({})
  await start($, on, { exitCode: 0, stdout: JSON.stringify([old]), stderr: '' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await textsOf(ui, 'error'))?.[1]).toMatch(/is dev up to date/)
  await ui.unmount()
})

const PLAN = [
  { content: 'Read the code', status: 'completed' },
  { content: 'Write the mod', status: 'in_progress' },
  { content: 'Open the PR', status: 'pending' },
] as const

type Cell = { props: Record<string, unknown>; children: { props: Record<string, unknown>; children: string[] }[] }

/** A drawn step row: its mark and text, each with the props of its Text, and its cells' flexShrink. */
async function stepAt(ui: Mounted, key: string) {
  const cells = ((await ui.find({ key }))?.children ?? []) as Cell[]
  const [mark, text] = cells.map(c => c.children[0])
  return { mark: mark?.children.join(''), markProps: mark?.props, text: text?.children.join(''), textProps: text?.props, shrink: cells.map(c => c.props.flexShrink) }
}

test('the task list: a small bar and its count on the meta line; the steps in the detail', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', plan: [...PLAN] })]))
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect((await ui.find({ type: 'Text', text: /^1\/3$/ }))?.props).toMatchObject({ size: 'footnote', tabular: true })
    expect(await ui.find({ type: 'Text', text: /Tasks/ })).toBeFalsy()
    // The bar: on the desktop a track, its done part grown by the steps done against those left; on the terminal whole cells
    const bar = await ui.find({ key: 'local_a:bar' })
    if (surface === 'terminal') {
      // The empty part thin and dim, so an empty bar reads as empty
      expect(((bar?.children[0] as Cell | undefined)?.children ?? []).map(c => [c.props.color, c.props.dimColor, c.children.join('')])).toEqual([['success', undefined, '━━'], ['inactive', true, '───']])
    } else {
      const track = bar?.children[0] as Cell | undefined
      // A faint track, not a theme grey that draws as a full bar
      expect(track?.props.backgroundColor).toMatch(/^rgba\(/)
      expect(track?.children.map(c => [c.props.flexGrow, c.props.backgroundColor])).toEqual([[1, 'success'], [2, undefined]])
    }
    // No step on the row until its detail opens
    expect(await ui.find({ key: 'local_a:plan' })).toBeFalsy()
    await unfold(ui)
    // A row per step: its mark and its text in cells of their own, the text cut to one line
    const steps = await Promise.all(PLAN.map((_, i) => stepAt(ui, `local_a:step${i}`)))
    expect(steps.map(s => s.shrink)).toEqual([[0, 1], [0, 1], [0, 1]])
    expect(steps.map(s => [s.mark, s.text])).toEqual([['●', 'Read the code'], ['◐', 'Write the mod'], ['○', 'Open the PR']])
    expect(steps.every(s => s.textProps?.wrap === 'truncate-end')).toBe(true)
    // Done recedes, the current step stands out in the section's color, pending stays quiet; the desktop in its tokens: muted not dimmed, semibold, Working's green
    if (surface === 'terminal') {
      expect(steps.map(s => [s.markProps?.color, s.markProps?.dimColor])).toEqual([['success', true], ['suggestion', false], ['subtle', false]])
      expect(steps.map(s => [s.textProps?.color, s.textProps?.weight, s.textProps?.dimColor])).toEqual([['inactive', 'regular', true], ['text', 'medium', false], ['subtle', 'regular', false]])
    } else {
      expect(steps.map(s => [s.markProps?.color, s.markProps?.dimColor])).toEqual([['subtle', false], ['success', false], ['subtle', false]])
      expect(steps.map(s => [s.textProps?.color, s.textProps?.weight, s.textProps?.dimColor])).toEqual([['subtle', 'regular', false], ['text', 'semibold', false], ['subtle', 'regular', false]])
    }
    // Above the buttons
    const order = (await ui.findAll({})).map(el => el.key ?? el.text)
    expect(order.indexOf('local_a:plan')).toBeGreaterThan(-1)
    expect(order.indexOf('local_a:plan')).toBeLessThan(order.indexOf('local_a:open'))
    await ui.press({ key: 'local_a:fold' })
    expect(await ui.find({ type: 'Text', text: 'Read the code' })).toBeFalsy()
    await ui.unmount()
  }
})

test('a long task list folds the done steps but the last into a count, and those past the room into more', async ($, on) => {
  const plan = Array.from({ length: 12 }, (_, i) => ({ content: `Step ${i}`, status: i < 6 ? 'completed' : i === 6 ? 'in_progress' : 'pending' }) as const)
  await start($, on, ok([worker({ state: 'busy', status: 'working', plan })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  const rows = ((await ui.find({ key: 'local_a:plan' }))?.children ?? []) as { props: { key?: string } }[]
  const shown = await Promise.all(rows.map(r => stepAt(ui, r.props.key ?? '')))
  expect(shown.map(s => `${s.mark} ${s.text}`)).toEqual(['● 5 done', '● Step 5', '◐ Step 6', '○ Step 7', '○ Step 8', '○ Step 9', '○ 2 more'])
  await ui.unmount()
})

test('a long task list by its shape: the count rows only where steps fold', async ($, on) => {
  const plan = (n: number, done: number, current: number | null) =>
    Array.from({ length: n }, (_, i) => ({ content: `S${i}`, status: i < done ? 'completed' : i === current ? 'in_progress' : 'pending' }) as const)
  const cases = [
    { plan: plan(7, 0, null), rows: ['○ S0', '○ S1', '○ S2', '○ S3', '○ S4', '○ S5', '○ S6'] },
    { plan: plan(8, 0, null), rows: ['○ S0', '○ S1', '○ S2', '○ S3', '○ S4', '○ S5', '○ 2 more'] },
    { plan: plan(10, 0, 0), rows: ['◐ S0', '○ S1', '○ S2', '○ S3', '○ S4', '○ S5', '○ 4 more'] },
    { plan: plan(10, 7, 7), rows: ['● 6 done', '● S6', '◐ S7', '○ S8', '○ S9'] },
    { plan: plan(9, 9, null), rows: ['● 8 done', '● S8'] },
  ]
  await start($, on, ok(cases.map((c, i) => worker({ id: `local_${i}`, state: 'busy', status: 'working', plan: [...c.plan] }))))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  for (const [i, c] of cases.entries()) {
    await unfold(ui, `local_${i}`)
    const rows = ((await ui.find({ key: `local_${i}:plan` }))?.children ?? []) as { props: { key?: string } }[]
    const shown = await Promise.all(rows.map(r => stepAt(ui, r.props.key ?? '')))
    expect(shown.map(s => `${s.mark} ${s.text}`)).toEqual(c.rows)
  }
  await ui.unmount()
})

test('a current step past the room of a long task list still shows, in the last row', async ($, on) => {
  const plan = Array.from({ length: 10 }, (_, i) => ({ content: `Step ${i}`, status: i < 3 ? 'completed' : i === 9 ? 'in_progress' : 'pending' }) as const)
  await start($, on, ok([worker({ state: 'busy', status: 'working', plan })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  const rows = ((await ui.find({ key: 'local_a:plan' }))?.children ?? []) as { props: { key?: string } }[]
  const shown = await Promise.all(rows.map(r => stepAt(ui, r.props.key ?? '')))
  expect(shown.map(s => `${s.mark} ${s.text}`)).toEqual(['● 2 done', '● Step 2', '○ Step 3', '○ Step 4', '○ Step 5', '◐ Step 9', '○ 3 more'])
  await ui.unmount()
})

test('no task list: no bar, no count, no steps', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ key: 'local_a:progress' })).toBeFalsy()
  await unfold(ui)
  expect(await ui.find({ key: 'local_a:plan' })).toBeFalsy()
  await ui.unmount()
})

test('needs you sorts first, says what it asks, and the band counts it', async ($, on) => {
  await start($, on, ok([
    worker({ state: 'busy', status: 'working', since: '12m' }),
    worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'needs you', statusText: 'allow Bash: rm -rf bin', since: '5m' }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const headers = (await ui.findAll({})).flatMap(el => (el.key?.endsWith(':header') ? [el.key] : []))
  expect(headers).toEqual(['local_b:header', 'local_a:header'])
  expect([await header(ui, 'local_b'), await header(ui)]).toEqual(['Worker B allow Bash: rm -rf bin 5m', 'Worker A working 12m'])
  // A working worker between tools says so, its row kept
  expect((await ui.findAll({ type: 'Text', text: /^(allow|needs you|working)/ })).map(t => t.text)).toEqual(['allow Bash: rm -rf bin', 'working'])
  await ui.unmount()
  const band = await $.ui.mount({ ...BAND, surface: 'desktop' })
  // The worker that needs you is not counted busy too
  expect((await band.find({ type: 'Text' }))?.text).toBe('1 needs you · 2 workers · 1 busy')
  // Only the needs you part is red
  const parts = (await band.find({ type: 'Text' }))?.children as { props: { color?: string; dimColor?: boolean }; children: string[] }[]
  expect(parts.map(p => [p.children.join(''), p.props.color ?? (p.props.dimColor ? 'dim' : '')])).toEqual([['1 needs you', 'error'], [' · 2 workers · 1 busy', 'dim']])
  await band.unmount()
})

test('a waiting worker says on what and for how long', async ($, on) => {
  await start($, on, ok([worker({ status: 'waiting', statusText: 'Auto-fix: PR #80', since: '4m' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await header(ui)).toBe('Worker A Auto-fix: PR #80 4m')
  expect((await ui.findAll({ type: 'Text', text: /^(waiting|Auto-fix)/ })).map(t => t.text)).toEqual(['Auto-fix: PR #80'])
  await ui.unmount()
})

const OPEN_PR = { number: 80, url: 'https://github.com/o/r/pull/80', state: 'OPEN' }
const CHECKS: Ran = {
  exitCode: 8,
  stdout: JSON.stringify([{ name: 'test', bucket: 'pass' }, { name: 'lint', bucket: 'fail' }, { name: 'e2e', bucket: 'pending' }]),
  stderr: '',
}

type El = string | { type: string; props?: Record<string, unknown>; children?: El[] }
const linkText = (el: El): string => (typeof el === 'string' ? el : el.type === 'Link' ? String(el.props?.label) : (el.children ?? []).map(linkText).join(''))
/** An element's Texts, Links by their label, and icons, an Svg by its alt (desktop), a glyph as its text (terminal). */
const readEl = (el: El): string[] => {
  if (typeof el === 'string') return []
  if (el.type === 'Svg') return [`[${String(el.props?.alt)}]`]
  if (el.type === 'Text' || el.type === 'Link') return [linkText(el)]
  return (el.children ?? []).flatMap(readEl)
}

/** A PR's row as read: its Texts and its icons. */
async function prRow(ui: Mounted, key = `local_a:${OPEN_PR.url}`) {
  const row = await ui.find({ key })
  return row === undefined ? undefined : (row.children as El[]).flatMap(readEl)
}

test("an open PR on the meta line: its icon and link, then its CI icon and the checks that make it; read once a minute", async ($, on) => {
  const { otherRuns, clock } = await start($, on, ok([worker({ state: 'busy', status: 'working', prs: [OPEN_PR] })]), { gh: CHECKS })
  const gh = () => otherRuns.filter(r => r.argv[0] === 'gh')
  expect(gh().map(r => r.argv)).toEqual([['gh', 'pr', 'checks', OPEN_PR.url, '--json', 'name,bucket']])
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  // A failed check outweighs a running one
  expect(await prRow(ui)).toEqual(['[PR #80 open]', 'PR #80', '[CI failed]', 'lint'])
  // One line: the PR never wraps, its check names cut
  expect((await ui.find({ key: `local_a:${OPEN_PR.url}` }))?.props).toMatchObject({ flexDirection: 'row', minWidth: 0 })
  expect((await ui.find({ key: `local_a:${OPEN_PR.url}` }))?.props.flexWrap).toBeUndefined()
  expect((await ui.find({ type: 'Text', text: 'lint' }))?.props.wrap).toBe('truncate-end')
  await ui.unmount()
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await prRow(terminal)).toEqual(['○', 'PR #80', '✗', 'lint'])
  expect((await terminal.findAll({ type: 'Text', text: /^[○✗]$/ })).map(t => t.props.color)).toEqual(['success', 'error'])
  await terminal.unmount()
  await clock.advance(58_000)
  expect(gh().length).toBe(1)
  await clock.advance(2000)
  expect(gh().length).toBe(2)
})

const checksOf = (...buckets: string[]): Ran => ({
  exitCode: 0,
  stdout: JSON.stringify(buckets.map((bucket, i) => ({ name: `c${i}`, bucket }))),
  stderr: '',
})

const ROLLUPS: [string, Ran, string[], string[]][] = [
  ['passed or skipped', checksOf('pass', 'skipping'), ['[CI passed]', 'c0 c1'], ['✓', 'c0 c1']],
  ['any running, none failed', checksOf('pass', 'pending', 'pending'), ['[CI running]', 'c1 c2'], ['…', 'c1 c2']],
  ['a bucket it does not know, as running', checksOf('pass', 'queued'), ['[CI running]', 'c1'], ['…', 'c1']],
  ['any failed or cancelled', checksOf('pass', 'cancel', 'pending'), ['[CI failed]', 'c1'], ['✗', 'c1']],
  ['every failed one named', checksOf('fail', 'cancel', 'pending'), ['[CI failed]', 'c0 c1'], ['✗', 'c0 c1']],
  ['no checks: no icon', checksOf(), [], []],
]
for (const [name, gh, desktop, terminal] of ROLLUPS) {
  test(`the CI icon sums up the checks: ${name}`, async ($, on) => {
    await start($, on, ok([worker({ state: 'busy', status: 'working', prs: [OPEN_PR] })]), { gh })
    for (const [surface, ci, state] of [['desktop', desktop, '[PR #80 open]'], ['terminal', terminal, '○']] as const) {
      const ui = await $.ui.mount({ ...PANE, surface })
      expect(await prRow(ui)).toEqual([state, 'PR #80', ...ci])
      await ui.unmount()
    }
  })
}

const CLOSED_PR = { ...OPEN_PR, number: 81, url: 'https://github.com/o/r/pull/81', state: 'CLOSED' }

test('a merged or closed PR shows its own icon and no CI icon', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', prs: [{ ...OPEN_PR, state: 'MERGED' }, CLOSED_PR] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await prRow(ui)).toEqual(['[PR #80 merged]', 'PR #80'])
  expect(await prRow(ui, `local_a:${CLOSED_PR.url}`)).toEqual(['[PR #81 closed]', 'PR #81'])
  // Each its own shape, not one recolored
  const sources = (await ui.findAll({ type: 'Svg' })).map(s => String(s.props.source).replace(/#[0-9A-F]{6}/gi, ''))
  expect(new Set(sources).size).toBe(2)
  await ui.unmount()
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await prRow(terminal)).toEqual(['◆', 'PR #80'])
  expect(await prRow(terminal, `local_a:${CLOSED_PR.url}`)).toEqual(['✕', 'PR #81'])
  expect((await terminal.findAll({ type: 'Text', text: /^[◆✕]$/ })).map(t => t.props.color)).toEqual(['merged', 'error'])
  await terminal.unmount()
})

test('checks read while a PR was open show no more once it merged', async ($, on) => {
  let prs = [OPEN_PR]
  const { clock } = await start($, on, () => ok([worker({ state: 'busy', status: 'working', prs })]), { gh: CHECKS })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await prRow(ui)).toEqual(['[PR #80 open]', 'PR #80', '[CI failed]', 'lint'])
  prs = [{ ...OPEN_PR, state: 'MERGED' }]
  await clock.advance(2000)
  expect(await prRow(ui)).toEqual(['[PR #80 merged]', 'PR #80'])
  await ui.unmount()
})

test("the meta line shows each PR's icons in order, line 1 none", async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', prs: [OPEN_PR, CLOSED_PR] })]), { gh: checksOf('pass') })
  for (const [surface, icons] of [['desktop', ['[PR #80 open]', '[CI passed]', '[PR #81 closed]']], ['terminal', ['○', '✓', '✕']]] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect((await prRow(ui, 'local_a:meta'))?.filter(t => icons.includes(t as never))).toEqual([...icons])
    expect((await prRow(ui, 'local_a:header'))?.filter(t => icons.includes(t as never))).toEqual([])
    await ui.unmount()
  }
})

test('checks that could not be read say why', async ($, on) => {
  await start($, on, ok([worker({ prs: [OPEN_PR] })]), { gh: 'spawn gh ENOENT' })
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    await unfold(ui)
    if (surface === 'desktop') {
      const row = await prRow(ui)
      expect(row?.slice(0, 2)).toEqual(['[PR #80 open]', 'PR #80'])
      expect(row?.[2]).toMatch(/^gh pr checks: .*ENOENT/)
    }
    // Dimmer than check names; the desktop's muted tone, no dim on top
    expect((await ui.find({ type: 'Text', text: /ENOENT/ }))?.props).toMatchObject({ color: 'subtle', dimColor: surface === 'terminal', wrap: 'truncate-end' })
    await ui.unmount()
  }
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
  await unfold(ui)
  await ui.press({ key: 'local_a:archive' })
  expect(texts).toEqual(['dev-board: archive Worker A (local_a): dev delivered exited 0'])
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
  await unfold(ui)
  expect(await ui.find({ key: 'local_a:archive' })).toBeFalsy()
  await ui.unmount()
})

test('Message… opens a field whose text the boss is asked to relay', async ($, on) => {
  const texts = submits(on)
  await start($, on, ok([worker({})]))
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    await unfold(ui)
    expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
    await ui.press({ key: 'local_a:message' })
    expect(await ui.find({ key: 'local_a:draft' })).toBeTruthy()
    await ui.input({ key: 'local_a:draft', text: ' add tests ' })
    expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
    await ui.unmount()
  }
  expect(texts).toEqual(['dev-board: message Worker A (local_a): add tests', 'dev-board: message Worker A (local_a): add tests'])
})

test('an empty message closes the field and sends nothing', async ($, on) => {
  const texts = submits(on)
  await start($, on, ok([worker({})]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await ui.press({ key: 'local_a:message' })
  await ui.input({ key: 'local_a:draft', text: '  ' })
  expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
  expect(texts).toEqual([])
  await ui.unmount()
})

test('the mobile app, with no field, has no Message…', async ($, on) => {
  await start($, on, ok([worker({})]))
  const ui = await $.ui.mount({ ...PANE, surface: 'mobile' })
  await unfold(ui)
  expect(await ui.find({ key: 'local_a:message' })).toBeFalsy()
  expect(await ui.find({ key: 'local_a:open' })).toBeTruthy()
  await ui.unmount()
})

test("Open opens the worker's session through the app's deep link", async ($, on) => {
  const toasts = collectToasts(on)
  const { otherRuns } = await start($, on, ok([worker({})]), { open: { exitCode: 1, stdout: '', stderr: 'no handler' } })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await ui.press({ key: 'local_a:open' })
  expect(otherRuns.filter(r => r.argv[0] === 'open').map(r => r.argv)).toEqual([['open', 'claude://code/continue?session=local_a']])
  expect(toasts).toEqual(['could not open Worker A: no handler'])
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

const DELIVERED: Ran = { exitCode: 0, stdout: 'delivered', stderr: '' }

test('Archive checks delivery again when pressed, and says why not', async ($, on) => {
  const texts = submits(on)
  const toasts = collectToasts(on)
  let delivery: Ran = DELIVERED
  await start($, on, ok([worker({ prs: [OPEN_PR] })]), { delivered: () => delivery })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  delivery = { exitCode: 1, stdout: '', stderr: 'uncommitted changes to tracked files' }
  await ui.press({ key: 'local_a:archive' })
  expect(texts).toEqual([])
  expect(toasts).toEqual(['Worker A not archived: uncommitted changes to tracked files'])
  await ui.unmount()
})

test('no Archive nor dev delivered for a waiting worker with a delivered PR; the same worker idle gets it', async ($, on) => {
  const toasts = collectToasts(on)
  let workers = [worker({ status: 'waiting', statusText: 'background: CI on PR #80', prs: [OPEN_PR] })]
  const { otherRuns, clock } = await start($, on, () => ok(workers), { delivered: DELIVERED })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  expect(otherRuns.filter(r => r.argv[1] === 'delivered').length).toBe(0)
  expect(await ui.find({ key: 'local_a:archive' })).toBeFalsy()
  expect(await ui.findAll({ type: 'Text', text: /not delivered/ })).toEqual([])
  workers = [worker({ prs: [OPEN_PR] })]
  await clock.advance(2000)
  await unfold(ui)
  expect(await ui.find({ key: 'local_a:archive' })).toBeTruthy()
  await ui.unmount()
  expect(toasts).toEqual([])
})

test('a worker going busy drops its delivery: Archive waits for a new check', async ($, on) => {
  let workers = [worker({ prs: [OPEN_PR] })]
  const { otherRuns, clock } = await start($, on, () => ok(workers), { delivered: DELIVERED })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  expect(await ui.find({ key: 'local_a:archive' })).toBeTruthy()
  workers = [worker({ state: 'busy', status: 'working', prs: [OPEN_PR] })]
  await clock.advance(2000)
  expect(await ui.find({ key: 'local_a:archive' })).toBeFalsy()
  workers = [worker({ prs: [OPEN_PR] })]
  await clock.advance(2000)
  // Checked again at once, not a minute after the last
  expect(otherRuns.filter(r => r.argv[1] === 'delivered').length).toBe(2)
  expect(await ui.find({ key: 'local_a:archive' })).toBeTruthy()
  await ui.unmount()
})

test('a dev delivered that cannot run says why, with no Archive', async ($, on) => {
  await start($, on, ok([worker({ prs: [OPEN_PR] })]), { delivered: 'spawn dev ENOENT' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  expect((await ui.find({ type: 'Text', text: /^not delivered/ }))?.text).toMatch(/^not delivered: dev delivered: .*ENOENT/)
  expect(await ui.find({ key: 'local_a:archive' })).toBeFalsy()
  await ui.unmount()
})

test('a worker with no worktree runs no dev delivered', async ($, on) => {
  const { otherRuns } = await start($, on, ok([worker({ worktree: '', prs: [OPEN_PR] })]))
  expect(otherRuns.filter(r => r.argv[1] === 'delivered')).toEqual([])
})

test('a dev without plans says so', async ($, on) => {
  const { plan: _, ...old } = worker({})
  await start($, on, { exitCode: 0, stdout: JSON.stringify([old]), stderr: '' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await textsOf(ui, 'error'))?.[1]).toMatch(/is dev up to date/)
  await ui.unmount()
})

test("a needs-action of a worker not blocked still shows", async ($, on) => {
  await start($, on, ok([worker({ status: 'idle', verdict: 'blocked', needsAction: 'pick one' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^⚠ needs:/ }))?.text).toBe('⚠ needs: pick one')
  await ui.unmount()
})

test('a blocked worker that needs you is counted there only, not blocked too', async ($, on) => {
  await start($, on, ok([
    worker({ state: 'blocked', status: 'needs you', statusText: 'Report format?' }),
    worker({ id: 'local_b', title: 'Worker B', state: 'blocked', status: 'blocked' }),
    worker({ id: 'local_c', title: 'Worker C', state: 'busy', status: 'working' }),
  ]))
  const band = await $.ui.mount({ ...BAND, surface: 'desktop' })
  expect((await band.find({ type: 'Text' }))?.text).toBe('1 needs you · 3 workers · 1 busy · 1 blocked')
  await band.unmount()
})

test('a question with options says it once: no needs line, no reply line restating it or listing its options', async ($, on) => {
  const format = { question: 'Report format?', header: '', multiSelect: false, options: [{ label: 'Per package', description: '' }, { label: 'Total only', description: '' }] }
  await start($, on, ok([
    worker({
      state: 'blocked',
      status: 'needs you',
      statusText: 'Report format?',
      verdict: 'blocked',
      detail: 'counted 144 .go files under internal/; awaiting report format',
      needsAction: 'pick a reply: Per package, or Total only',
      lastLines: ['Counted 144 files.', 'Pick a reply: **Per package**, or `Total only`.'],
      ask: { toolUseId: 'w1', questions: [format], askedAt: 0, answerable: false, reply: true },
    }),
    // Named among more words, the options are kept
    worker({
      id: 'local_b',
      title: 'Worker B',
      state: 'blocked',
      status: 'needs you',
      statusText: 'Report format?',
      lastLines: ['Per package lists each one; Total only is a single number for the whole tree.'],
      ask: { toolUseId: 'w2', questions: [format], askedAt: 0, answerable: false, reply: true },
    }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await unfold(ui, 'local_b')
  expect(await ui.find({ type: 'Text', text: /needs:/ })).toBeFalsy()
  expect(await ui.find({ type: 'Text', text: /^Pick a reply/ })).toBeFalsy()
  // The question's own rows say it; line 1 does not again
  expect((await ui.findAll({ type: 'Text', text: /^Report format\?$/ })).length).toBe(2)
  expect(await header(ui)).toBe('Worker A')
  expect((await ui.find({ type: 'Text', text: /^Counted 144 files/ }))?.text).toBe('Counted 144 files.')
  expect((await ui.find({ type: 'Text', text: /^Per package lists/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('a reply line listing the options is left out, by itself; a short comparison of them is kept', async ($, on) => {
  const yes = { question: 'Ship it?', header: '', multiSelect: false, options: [{ label: 'Yes (Recommended)', description: '' }, { label: 'No', description: '' }] }
  const format = { question: 'Report format?', header: '', multiSelect: false, options: [{ label: 'Per package', description: '' }, { label: 'Total only', description: '' }] }
  const asks = (id: string, q: typeof yes, lastLines: string[]) =>
    worker({ id, title: id, state: 'blocked', status: 'needs you', statusText: q.question, lastLines, ask: { toolUseId: id, questions: [q], askedAt: 0, answerable: false, reply: true } })
  await start($, on, ok([
    asks('local_a', format, ['Kept one.', 'Which one: **Per package** or `Total only`?']),
    asks('local_b', yes, ['Kept two.', 'Pick one: Yes (Recommended) or No.']),
    asks('local_c', format, ['Per package is slower; Total only skips files.']),
    asks('local_d', format, ['So please pick one: Per package, or Total only']),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  for (const id of ['local_a', 'local_b', 'local_c', 'local_d']) await unfold(ui, id)
  expect(await ui.find({ type: 'Text', text: /^Which one/ })).toBeFalsy()
  expect(await ui.find({ type: 'Text', text: /^Pick one/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^Per package is slower/ }))?.text).toBeTruthy()
  expect((await ui.find({ type: 'Text', text: /^So please/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('beside options a reply line saying the need is left out, as the need is; without options both show', async ($, on) => {
  const format = { question: 'Report format?', header: '', multiSelect: false, options: [{ label: 'Per package', description: '' }, { label: 'Total only', description: '' }] }
  const need = { needsAction: 'choose how the report groups files', lastLines: ['Choose how the report groups files.'] }
  await start($, on, ok([
    worker({ status: 'idle', ...need, ask: { toolUseId: 'w1', questions: [format], askedAt: 0, answerable: false, reply: true } }),
    worker({ id: 'local_b', title: 'Worker B', status: 'idle', ...need, ask: { toolUseId: 'w2', questions: [{ ...format, options: [] }], askedAt: 0, answerable: false, reply: true } }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await unfold(ui, 'local_b')
  expect((await ui.findAll({ type: 'Text', text: /^Choose how/ })).length).toBe(1)
  expect((await ui.findAll({ type: 'Text', text: /needs:/ })).map(t => t.text)).toEqual(['⚠ needs: choose how the report groups files'])
  await ui.unmount()
})

test('a need beside a question without options still shows', async ($, on) => {
  const open = { question: 'What should the report say?', header: '', multiSelect: false, options: [] }
  await start($, on, ok([worker({ status: 'idle', needsAction: 'describe the report', ask: { toolUseId: 'w1', questions: [open], askedAt: 0, answerable: false, reply: true } })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^⚠ needs:/ }))?.text).toBe('⚠ needs: describe the report')
  await ui.unmount()
})

test('Open that cannot run says so', async ($, on) => {
  const toasts = collectToasts(on)
  await start($, on, ok([worker({})]), { open: 'denied by a rule' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await ui.press({ key: 'local_a:open' })
  expect(toasts).toEqual(['could not open Worker A: dev-board: $.process.run: denied by a rule'])
  await ui.unmount()
})

const BY_STATUS = [
  worker({ id: 'local_i', title: 'Idle', status: 'idle' }),
  worker({ id: 'local_s', title: 'Stopped', state: 'stopped', status: 'stopped' }),
  worker({ id: 'local_w', title: 'Waiting', status: 'waiting' }),
  worker({ id: 'local_r', title: 'Review', status: 'review ready' }),
  worker({ id: 'local_b', title: 'Blocked', state: 'blocked', status: 'blocked' }),
  worker({ id: 'local_k', title: 'Working', state: 'busy', status: 'working' }),
  worker({ id: 'local_t', title: 'Stuck', state: 'busy', status: 'stuck' }),
  worker({ id: 'local_l', title: 'Limited', state: 'busy', status: 'rate-limited' }),
  worker({ id: 'local_n', title: 'Needs', state: 'busy', status: 'needs you' }),
]

test('idle and stopped workers share one line that opens to a line each; the rest get rows', async ($, on) => {
  const { clock } = await start($, on, ok(BY_STATUS.map(w => (w.status === 'idle' ? { ...w, plan: [PLAN[0]], since: '15m' } : w))))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ key: 'local_i:fold' })).toBeFalsy()
  expect((await ui.find({ key: 'idle:fold' }))?.text).toBe('▸ Idle, Stopped')
  expect((await ui.find({ key: 'idle:fold' }))?.props).toMatchObject({ plain: true, dimColor: true })
  for (const id of ['local_w', 'local_r', 'local_b', 'local_k', 'local_t', 'local_l', 'local_n']) {
    expect(await ui.find({ key: `${id}:row` })).toBeTruthy()
  }
  await ui.press({ key: 'idle:fold' })
  // Open rows sit one step in, their dots under the toggle's text
  expect((await ui.find({ key: 'local_i:header' }))?.props.paddingLeft).toBe('sm')
  // Open, its lines name them: the line counts them only
  expect((await ui.find({ key: 'idle:fold' }))?.text).toBe('▾ 2 idle')
  // A line each: dot, name (dim, regular: they ask nothing), task count, time
  expect(await header(ui, 'local_i')).toBe('● Idle 1/1 15m')
  expect((await ui.find({ key: 'local_i:fold' }))?.props.dimColor).toBe(true)
  expect((await ui.find({ type: 'Text', text: 'Idle' }))?.props.weight).toBe('regular')
  expect(await ui.find({ key: 'local_i:open' })).toBeFalsy()
  // Its name opens its row with its detail
  await ui.press({ key: 'local_i:fold' })
  expect(await ui.find({ key: 'local_i:row' })).toBeTruthy()
  expect(await ui.find({ key: 'local_i:open' })).toBeTruthy()
  // One quiet worker left: its line is drawn directly, with no toggle
  expect(await ui.find({ key: 'idle:fold' })).toBeFalsy()
  expect(await ui.find({ key: 'local_s:header' })).toBeTruthy()
  // Closed again, it goes back into the line, which stays open across a poll
  await ui.press({ key: 'local_i:fold' })
  await clock.advance(2000)
  expect(await ui.find({ key: 'local_i:row' })).toBeFalsy()
  expect((await ui.find({ key: 'idle:fold' }))?.text).toBe('▾ 2 idle')
  await ui.press({ key: 'idle:fold' })
  expect((await ui.find({ key: 'idle:fold' }))?.text).toBe('▸ Idle, Stopped')
  await ui.unmount()
  // The terminal's open line still names them
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await terminal.press({ key: 'idle:fold' })
  expect((await terminal.find({ key: 'idle:fold' }))?.text).toBe('▾ 2 idle')
  expect((await terminal.find({ key: 'local_i:header' }))?.props.paddingLeft).toBeUndefined()
  await terminal.unmount()
})

test('an idle worker with a health note keeps its own row, the note under it with ⚠', async ($, on) => {
  await start($, on, ok([worker({ state: 'stopped', status: 'stopped', notes: [RESUME] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ key: 'idle:fold' })).toBeFalsy()
  expect(await header(ui)).toBe('Worker A stopped')
  expect((await ui.find({ type: 'Text', text: /^⚠ resume due:/ }))?.text).toBe('⚠ resume due: rate limit reset 18:05: resume it')
  await ui.unmount()
})

test('a status this mod does not know shows under Working', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'paused' as Worker['status'] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.findAll({ type: 'Text', text: /^(WORKING$|paused)/ })).map(t => t.text)).toEqual(['WORKING', 'paused'])
  expect(await header(ui)).toBe('Worker A paused')
  await unfold(ui)
  expect(await ui.find({ key: 'local_a:open' })).toBeTruthy()
  await ui.unmount()
})

test('an answered worker dev reads as idle joins the idle line: no need, no detail on line 1', async ($, on) => {
  // What dev prints once a plain wrap_up and a bare IDLE marker end the turn the app's summary called blocked
  await start($, on, ok([worker({ name: 'demo-ask', lastLines: ['IDLE'], recap: 'Report format chosen: per package.' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  // Alone in the idle line: drawn as its line, no toggle
  expect(await ui.find({ key: 'idle:fold' })).toBeFalsy()
  expect((await header(ui))?.startsWith('● demo-ask')).toBe(true)
  expect(await ui.find({ key: 'local_a:row' })).toBeFalsy()
  expect(await ui.find({ type: 'Text', text: /needs:|awaiting/ })).toBeFalsy()
  await ui.unmount()
})

test('the detail shows the title where it differs from the name, and the recap where no reply line shows', async ($, on) => {
  await start($, on, ok([
    worker({ name: 'demo-ask', title: 'Board demo: blocked wrap_up question', lastLines: ['**IDLE**'], recap: 'Report format chosen: **per package**.' }),
    worker({ id: 'local_b', name: 'same', title: 'same', lastLines: ['Pushed.', 'IDLE'], recap: 'Pushed the fix.' }),
    // A dev older than this mod prints no recap
    worker({ id: 'local_c', title: 'Worker C', lastLines: ['IDLE'], recap: undefined }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  for (const id of ['local_a', 'local_b', 'local_c']) await unfold(ui, id)
  const detail = async (id: string) => (await textsOf(ui, `${id}:detail`))?.filter(t => !['Open', 'Message…'].includes(t))
  expect(await detail('local_a')).toEqual(['Board demo: blocked wrap_up question', 'feature/a', 'Report format chosen: per package.'])
  // The whole recap, wrapped; a reply line is cut to one row
  expect(await ui.find({ type: 'Text', text: /^Report format/ })).toMatchObject({ props: { wrap: 'wrap', color: 'subtle' } })
  expect(await detail('local_b')).toEqual(['feature/a', 'Pushed.'])
  expect(await detail('local_c')).toEqual(['feature/a'])
  await ui.unmount()
})

test("only the newest reply's last two lines show", async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', lastLines: ['one', 'two', 'three'] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  expect(await ui.find({ type: 'Text', text: /^one$/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^two$/ }))?.text).toBeTruthy()
  expect((await ui.find({ type: 'Text', text: /^three$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test("line 1 says the status, else the verdict's detail, else the newest reply line", async ($, on) => {
  await start($, on, ok([
    worker({ status: 'waiting', statusText: 'CI on PR #80', verdict: 'review_ready', detail: 'PR #80 ready', lastLines: ['Pushed.'] }),
    worker({ id: 'local_b', title: 'Worker B', status: 'waiting', verdict: 'review_ready', detail: 'PR #81 ready', lastLines: ['Pushed.'] }),
    worker({ id: 'local_c', title: 'Worker C', status: 'waiting', lastLines: ['Pushed.', '**IDLE**'] }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await Promise.all(['local_a', 'local_b', 'local_c'].map(id => header(ui, id)))).toEqual(['Worker A CI on PR #80', 'Worker B PR #81 ready', 'Worker C Pushed.'])
  expect(await ui.find({ type: 'Text', text: /review|PR #80 ready/ })).toBeFalsy()
  await ui.unmount()
})

test('a detail opened or closed sticks across polls and status changes, per worker', async ($, on) => {
  let workers = [worker({ state: 'busy', status: 'working' }), worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'working' }), worker({ id: 'local_c', title: 'Worker C' })]
  const { clock } = await start($, on, () => ok(workers))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const opened = async () => Promise.all(['local_a', 'local_b', 'local_c'].map(async id => (await ui.find({ key: `${id}:detail` })) !== undefined))
  await unfold(ui)
  await unfold(ui, 'local_c')
  expect(await opened()).toEqual([true, false, true])

  workers = [worker({ status: 'waiting', since: '3m' }), worker({ id: 'local_b', title: 'Worker B', status: 'review ready', since: '1m' }), worker({ id: 'local_c', title: 'Worker C', state: 'stopped', status: 'stopped' })]
  await clock.advance(4000)
  expect(await opened()).toEqual([true, false, true])
  await ui.unmount()

  const again = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await again.find({ key: 'local_a:detail' })).toBeTruthy()
  expect(await again.find({ key: 'local_b:detail' })).toBeFalsy()
  await again.unmount()
})

test('sections group the workers by status, with counts, needs you first; empty ones hidden', async ($, on) => {
  let workers = BY_STATUS
  const { clock } = await start($, on, () => ok(workers))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  // Section headers, their counts and the rows' first lines, in order: a row by its name
  const rows = async () => {
    const els = (await ui.findAll({})).filter(el => el.key?.endsWith(':header') || (el.type === 'Text' && /^(NEEDS YOU|WORKING|WAITING|IDLE|\d+)$/.test(el.text)))
    return Promise.all(els.map(async el => (el.key === undefined ? el.text : (await header(ui, el.key.replace(/:header$/, '')))?.split(' ')[0])))
  }
  expect(await rows()).toEqual([
    'NEEDS YOU',
    '1',
    'Needs',
    'WORKING',
    '3',
    'Working',
    'Stuck',
    'Limited',
    'WAITING',
    '3',
    'Waiting',
    'Review',
    'Blocked',
    'IDLE',
    '2',
  ])
  // The count beside its header, not in the header's color
  expect((await ui.find({ type: 'Text', text: /^2$/ }))?.props.color).toBe('subtle')

  workers = BY_STATUS.filter(w => w.status === 'idle' || w.status === 'working')
  await clock.advance(2000)
  expect(await rows()).toEqual(['WORKING', '1', 'Working', 'IDLE', '1', '●'])
  await ui.unmount()
})

test('a message being typed keeps its row as the worker goes idle; closing the detail keeps the field', async ($, on) => {
  let workers = [worker({ state: 'busy', status: 'working' })]
  const { clock } = await start($, on, () => ok(workers))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await ui.press({ key: 'local_a:message' })
  await ui.press({ key: 'local_a:fold' })
  workers = [worker({})]
  await clock.advance(2000)
  expect(await ui.find({ key: 'local_a:draft' })).toBeTruthy()
  expect(await ui.find({ key: 'idle:fold' })).toBeFalsy()
  await ui.press({ key: 'local_a:cancel' })
  expect(await ui.find({ key: 'idle:fold' })).toBeFalsy()
  expect(await ui.find({ key: 'local_a:header' })).toBeTruthy()
  await ui.unmount()
})

test('Message… on a Needs you row opens under line 1; on the terminal under line 1 whatever the detail; Cancel brings the actions back', async ($, on) => {
  await start($, on, ok(BY_STATUS))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_n:message' })
  expect(await ui.find({ key: 'local_n:compose' })).toBeTruthy()
  expect(JSON.stringify((await ui.find({ key: 'local_n:detail' }))?.children ?? '')).not.toContain('compose')
  await ui.unmount()
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await unfold(terminal, 'local_k')
  await terminal.press({ key: 'local_k:message' })
  expect(JSON.stringify((await terminal.find({ key: 'local_k:detail' }))?.children)).not.toContain('local_k:compose')
  expect((await terminal.find({ key: 'local_k:draft' }))?.props).toMatchObject({ label: 'Message Working' })
  expect((await terminal.find({ key: 'local_k:draft' }))?.props.placeholder).toBeUndefined()
  await terminal.unmount()
})

test('a field moved while typing (detail closed, or Working → Waiting) is drawn again with the typed text, and Send sends it', async ($, on) => {
  const texts = submits(on)
  let workers = [worker({ state: 'busy', status: 'working' })]
  const { clock } = await start($, on, () => ok(workers))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await ui.press({ key: 'local_a:message' })
  await ui.input({ key: 'local_a:draft', text: 'half typed', kind: 'change' })
  // Detail closed: the field moves under line 1
  await ui.press({ key: 'local_a:fold' })
  expect((await ui.find({ key: 'local_a:draft' }))?.props.value).toBe('half typed')
  // Detail open and the worker waits: its buttons move to line 1, the field with them
  await ui.press({ key: 'local_a:fold' })
  expect((await ui.find({ key: 'local_a:draft' }))?.props.value).toBe('half typed')
  workers = [worker({ state: 'busy', status: 'waiting', statusText: 'Auto-fix: PR #80' })]
  await clock.advance(2000)
  expect((await ui.find({ key: 'local_a:draft' }))?.props.value).toBe('half typed')
  await ui.press({ key: 'local_a:send' })
  expect(texts).toEqual(['dev-board: message Worker A (local_a): half typed'])
  await ui.unmount()
})

test('a verdict the status says, with no detail, shows no line', async ($, on) => {
  await start($, on, ok([worker({ state: 'blocked', status: 'blocked', verdict: 'blocked' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.findAll({ type: 'Text', text: /blocked/ })).map(t => t.text)).toEqual(['blocked'])
  await ui.unmount()
})

test('Open, Message… and Archive are text buttons (dim but on the desktop): on line 1 for Needs you and Waiting rows, else in the detail', async ($, on) => {
  await start($, on, ok([...BY_STATUS, worker({ id: 'local_p', title: 'Delivered', status: 'review ready', prs: [OPEN_PR] })]), { delivered: DELIVERED })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const onLine1 = async (id: string) => JSON.stringify((await ui.find({ key: `${id}:header` }))?.children).includes(`${id}:open`)
  expect(await Promise.all(['local_n', 'local_w', 'local_b', 'local_p', 'local_k'].map(onLine1))).toEqual([true, true, true, true, false])
  expect(await ui.find({ key: 'local_k:open' })).toBeFalsy()
  await unfold(ui, 'local_k')
  await unfold(ui, 'local_i')
  expect(JSON.stringify((await ui.find({ key: 'local_k:detail' }))?.children)).toContain('local_k:open')
  // A Needs you row's detail holds no second set
  await unfold(ui, 'local_n')
  expect((await ui.findAll({ type: 'Button' })).filter(b => b.key === 'local_n:open').length).toBe(1)
  const looks = async (key: string) => {
    const { plain, dimColor, variant } = (await ui.find({ key }))?.props ?? {}
    return [plain, dimColor, variant]
  }
  const keys = ['local_n:open', 'local_w:message', 'local_k:open', 'local_k:message', 'local_i:open', 'local_p:archive']
  // Full strength on the desktop, so they don't read as the status text beside them
  expect(await Promise.all(keys.map(looks))).toEqual(keys.map(() => [true, false, undefined]))
  // Archive shows for a delivered worker not busy with a PR, after Open and Message…
  const row = async (id: string) => (await ui.findAll({ type: 'Button' })).map(b => b.key).filter(k => k?.startsWith(`${id}:`) && k !== `${id}:fold`)
  expect(await row('local_i')).toEqual(['local_i:open', 'local_i:message'])
  expect(await row('local_p')).toEqual(['local_p:open', 'local_p:message', 'local_p:archive'])
  await ui.unmount()
})

test("on a row asking a question, Open and Message… are plain text (dim on the terminal): the question's options are its buttons", async ($, on) => {
  await start($, on, ok([asking({ toolUseId: 'tu1', questions: [DB], askedAt: 1, answerable: true })]))
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    const looks = async (key: string) => {
      const { plain, dimColor, variant } = (await ui.find({ key }))?.props ?? {}
      return [plain, dimColor, variant]
    }
    expect(await looks('local_a:open')).toEqual([true, surface === 'terminal', undefined])
    expect(await looks('local_a:message')).toEqual([true, surface === 'terminal', undefined])
    await ui.unmount()
  }
})

test("a reply's last lines drop their inline markdown, code kept as is", async ($, on) => {
  const lines = ['- Grant **full-screen** control (`request_full_control`)', '* keep `__init__` as is, __bold__', '**Done**: src/**/*.ts', '```']
  await start($, on, ok([worker({ state: 'busy', status: 'working', lastLines: lines })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  // The closing fence is skipped, so the two lines before it show
  expect(await ui.find({ type: 'Text', text: /^Grant/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^keep/ }))?.text).toBe('keep __init__ as is, bold')
  expect((await ui.find({ type: 'Text', text: /^Done/ }))?.text).toBe('Done: src/**/*.ts')
  await ui.unmount()
})

test('a list line drops its marker and bold, code kept as is', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', lastLines: ['- Grant **full-screen** control (`request_full_control`)'] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  expect((await ui.find({ type: 'Text', text: /^Grant/ }))?.text).toBe('Grant full-screen control (request_full_control)')
  await ui.unmount()
})

test('a time of 0m reads now', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', since: '0m' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^now$/ }))?.text).toBeTruthy()
  expect(await ui.find({ type: 'Text', text: /0m/ })).toBeFalsy()
  await ui.unmount()
})

test('rows shrink to the pane: the name and what it says are cut, the time kept', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', statusText: 'Bash', since: '5m' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ key: 'local_a:row' }))?.props).toMatchObject({ flexDirection: 'row', minWidth: 0 })
  const line = await ui.find({ key: 'local_a:header' })
  expect(line?.props).toMatchObject({ flexDirection: 'row', minWidth: 0 })
  expect((line?.children as { props: Record<string, unknown> }[]).map(c => [c.props.flexShrink, c.props.minWidth])).toEqual([[1, 0], [1, 0], [0, undefined]])
  for (const text of ['Worker A', 'Bash']) {
    expect((await ui.find({ type: 'Text', text }))?.props.wrap).toBe('truncate-end')
  }
  await ui.unmount()
})

test("a review-ready row says its detail once, on line 1", async ($, on) => {
  await start($, on, ok([worker({ status: 'review ready', statusText: 'PR #80 green', verdict: 'review_ready', detail: 'PR #80 green' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.findAll({ type: 'Text', text: /review|green/ })).map(t => t.text)).toEqual(['review ready · PR #80 green'])
  await ui.unmount()
})

test('what line 1 says is one cut line, without inline markdown', async ($, on) => {
  const needs = 'run `dev delivered` on **feature/a**, then merge PR #80 once its checks pass and the review is in'
  await start($, on, ok([worker({ state: 'blocked', status: 'blocked', statusText: needs, needsAction: needs, verdict: 'blocked' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const status = await ui.find({ type: 'Text', text: /^blocked · / })
  expect(status?.text).toBe('blocked · run dev delivered on feature/a, then merge PR #80 once its checks pass and the review is in')
  expect(status?.props).toMatchObject({ wrap: 'truncate-end', size: 'footnote', color: 'subtle' })
  await ui.unmount()
})

test('a cut Text holds only strings: a Text nested in it wraps on the desktop', async ($, on) => {
  await start($, on, ok([...BY_STATUS, worker({ id: 'local_p', plan: [...PLAN], since: '5m', statusText: 'x', lastLines: ['**done**'], verdict: 'completed', detail: 'ok' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui, 'local_p')
  const cut = (await ui.findAll({ type: 'Text' })).filter(t => t.props.wrap === 'truncate-end')
  expect(cut.length).toBeGreaterThan(BY_STATUS.length)
  expect(cut.filter(t => t.children.some(c => typeof c !== 'string')).map(t => t.text)).toEqual([])
  await ui.unmount()
})

test('the name is the plain button that opens the detail, semibold and cut to fit', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const name = await ui.find({ key: 'local_a:fold' })
  expect(name?.props).toMatchObject({ plain: true })
  expect(name?.text).toBe('Worker A')
  expect((await ui.find({ type: 'Text', text: 'Worker A' }))?.props).toMatchObject({ weight: 'semibold', wrap: 'truncate-end' })
  await ui.press({ key: 'local_a:fold' })
  expect(await ui.find({ key: 'local_a:detail' })).toBeTruthy()
  await ui.press({ key: 'local_a:fold' })
  expect(await ui.find({ key: 'local_a:detail' })).toBeFalsy()
  await ui.unmount()
})

test('a reply line saying what the status says is left out', async ($, on) => {
  const needs = 'waiting for the user to confirm the **release** of v0.40.0 before tagging it'
  await start($, on, ok([
    worker({ state: 'blocked', status: 'blocked', statusText: needs, lastLines: ['Tests pass.', 'PR #85 open.', 'Blocked: waiting for the user to confirm the release of v0.40.0, then tag'] }),
    worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'working', statusText: 'Bash', lastLines: ['Bash failed: exit 1'] }),
    worker({ id: 'local_c', title: 'Worker C', state: 'blocked', status: 'blocked', statusText: 'pick one', lastLines: ['Two ways.', 'Blocked: **pick one**.'] }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  for (const id of ['local_a', 'local_b', 'local_c']) await unfold(ui, id)
  expect(await ui.find({ type: 'Text', text: /^Blocked:/ })).toBeFalsy()
  // An earlier line takes its slot
  expect((await ui.find({ type: 'Text', text: /^Tests pass/ }))?.text).toBe('Tests pass.')
  expect((await ui.find({ type: 'Text', text: /^PR #85/ }))?.text).toBe('PR #85 open.')
  // A short one, its end punctuation aside
  expect(await ui.find({ type: 'Text', text: /pick one\./ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^Two ways/ }))?.text).toBe('Two ways.')
  // Only alike in a word: kept
  expect((await ui.find({ type: 'Text', text: /^Bash failed/ }))?.text).toBe('Bash failed: exit 1')
  await ui.unmount()
})

test('a reply line saying what a busy status says is left out, its background count aside', async ($, on) => {
  await start($, on, ok([
    worker({ state: 'busy', status: 'working', statusText: 'Count hook files · 2 background running', lastLines: ['Done.', 'Count hook files.'] }),
    // The count alone strips to nothing, which repeats nothing
    worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'working', statusText: '1 background running', lastLines: ['Waiting on it.', '1 background running'] }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await unfold(ui, 'local_b')
  expect(await ui.find({ type: 'Text', text: /^Count hook files\.$/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: 'Done.' }))?.text).toBeTruthy()
  expect(await textsOf(ui, 'local_b:detail')).toEqual(['feature/a', 'Waiting on it.', '1 background running'])
  await ui.unmount()
})

test('a row: the dot beside its lines, line 1, the meta line, then the detail; no borders, no padding', async ($, on) => {
  await start($, on, ok([worker({ state: 'blocked', status: 'blocked', statusText: 'pick one', context: 10, lastLines: ['Done.'], plan: [...PLAN] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  const row = await ui.find({ key: 'local_a:row' })
  expect((row?.children as { type: string; props: { key?: string } }[]).map(c => c.props.key ?? c.type)).toEqual(['Text', 'local_a:rows'])
  const rows = await ui.find({ key: 'local_a:rows' })
  expect(rows?.props).toMatchObject({ flexDirection: 'column', rowGap: 'xs' })
  expect((rows?.children as { props: { key?: string } }[]).map(c => c.props.key)).toEqual(['local_a:header', 'local_a:meta', 'local_a:detail'])
  const shown = JSON.stringify(rows?.children)
  for (const part of ['pick one', '10%', 'feature/a', 'Done.', 'Write the mod', 'local_a:open']) {
    expect(shown).toContain(part)
  }
  const boxes = await ui.findAll({ type: 'Box' })
  expect(boxes.filter(b => b.props.borderStyle !== undefined || b.props.paddingLeft !== undefined || b.props.marginTop !== undefined)).toEqual([])
  await ui.unmount()
})

test('Message… opens one row: a short field, Send and Cancel', async ($, on) => {
  const texts = submits(on)
  const toasts = collectToasts(on)
  await start($, on, ok([worker({ state: 'busy', status: 'working', name: 'demo-waiting' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await ui.press({ key: 'local_a:message' })
  const field = await ui.find({ key: 'local_a:draft' })
  // Named by its label, not its key
  expect(field?.props).toMatchObject({ label: 'Message', placeholder: 'what to tell demo-waiting…', submitLabel: 'send', autoFocus: true })
  expect((await ui.find({ key: 'local_a:send' }))?.props).toMatchObject({ label: 'Send', variant: 'primary' })
  expect((await ui.find({ key: 'local_a:cancel' }))?.props).toMatchObject({ label: 'Cancel', plain: true })
  const row = await ui.find({ key: 'local_a:compose' })
  expect(row?.props.flexDirection).toBe('row')
  // The field takes the row's free width
  const slot = (await ui.findAll({ type: 'Box' })).find(b => b.children.some(c => (c as { props?: { key?: string } }).props?.key === 'local_a:draft'))
  expect(slot?.props).toMatchObject({ flexGrow: 1, minWidth: 0 })
  // Desktop: at the end of the open detail, in place of the actions row
  expect(JSON.stringify((await ui.find({ key: 'local_a:detail' }))?.children)).toContain('local_a:compose')
  expect(await ui.find({ key: 'local_a:actions' })).toBeFalsy()

  // Send sends what was typed
  await ui.input({ key: 'local_a:draft', text: ' add tests ', kind: 'change' })
  await ui.press({ key: 'local_a:send' })
  expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
  expect(texts).toEqual(['dev-board: message demo-waiting (local_a): add tests'])

  // Each surface's field sends its own text
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await ui.press({ key: 'local_a:message' })
  await ui.input({ key: 'local_a:draft', text: 'from the desktop', kind: 'change' })
  await terminal.input({ key: 'local_a:draft', text: 'from the terminal', kind: 'change' })
  await ui.press({ key: 'local_a:send' })
  expect(texts.at(-1)).toBe('dev-board: message demo-waiting (local_a): from the desktop')
  await terminal.unmount()
  texts.pop()

  // Cancel closes it, sending nothing; opened again, the old text is gone
  await ui.press({ key: 'local_a:message' })
  await ui.input({ key: 'local_a:draft', text: 'never mind', kind: 'change' })
  await ui.press({ key: 'local_a:cancel' })
  expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
  await ui.press({ key: 'local_a:message' })
  await ui.press({ key: 'local_a:send' })
  // No text read (typed before a reload of the mod, say): kept open, saying so
  expect(await ui.find({ key: 'local_a:draft' })).toBeTruthy()
  expect(toasts.at(-1)).toBe('no text read for demo-waiting: press Enter in the field to send it')
  await ui.press({ key: 'local_a:cancel' })
  expect(texts).toEqual(['dev-board: message demo-waiting (local_a): add tests'])

  await ui.unmount()

  // The terminal keeps its Message… button beside the field: closed by Message… again, its text goes too
  const tty = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await tty.press({ key: 'local_a:message' })
  await tty.input({ key: 'local_a:draft', text: 'stale', kind: 'change' })
  await tty.press({ key: 'local_a:message' })
  await tty.press({ key: 'local_a:message' })
  await tty.press({ key: 'local_a:send' })
  await tty.press({ key: 'local_a:cancel' })
  expect(texts).toEqual(['dev-board: message demo-waiting (local_a): add tests'])
  await tty.unmount()
})

const DB = { question: 'Which DB?', header: 'DB', multiSelect: false, options: [{ label: 'Postgres', description: 'relational' }, { label: 'Mongo', description: '' }] }
const FEATURES = { question: 'Which features?', header: 'Features', multiSelect: true, options: [{ label: 'Auth', description: 'sign-in' }, { label: 'Search', description: 'full text' }] }
const asking = (ask: Worker['ask']) => worker({ state: 'busy', status: 'needs you', statusText: 'Which DB? (Postgres / Mongo)', ask })

test('a question shows under line 1: header and question, its options one per row where any is described (button, then its description beside it), the first leading', async ($, on) => {
  await start($, on, ok([asking({ toolUseId: 'tu1', questions: [DB], askedAt: 1, answerable: true })]))
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect((await ui.find({ type: 'Text', text: /^DB$/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /^Which DB\?$/ }))?.text).toBeTruthy()
    const options = await ui.find({ key: 'local_a:q0:options' })
    expect(options?.props.flexDirection).toBe('column')
    expect((options?.children as { props: { key?: string } }[]).map(c => c.props.key)).toEqual(['local_a:q0:o0', 'local_a:q0:o1'])
    const pick = async (oi: number) => {
      const { label, variant } = (await ui.find({ key: `local_a:q0:o${oi}:pick` }))?.props ?? {}
      return [label, variant]
    }
    expect(await pick(0)).toEqual(['Postgres', 'primary'])
    expect(await pick(1)).toEqual(['Mongo', undefined])
    // The description sits beside its option, the label not repeated; an option without one has no caption
    const about = await ui.find({ type: 'Text', text: /^relational$/ })
    expect([about?.text, about?.props.size, about?.props.color, about?.props.wrap]).toEqual(['relational', surface === 'terminal' ? 'caption' : 'footnote', 'subtle', 'truncate-end'])
    expect(await ui.find({ type: 'Text', text: /Mongo/ })).toBeFalsy()
    // The question replaces the status row that says it in one line; one press answers it, so no Send
    expect(await ui.find({ type: 'Text', text: /Postgres \/ Mongo/ })).toBeFalsy()
    expect(await ui.find({ key: 'local_a:answer' })).toBeFalsy()
    await ui.unmount()
  }
})

const SHIP = { question: 'Ship it?', header: '', multiSelect: false, options: [{ label: 'Yes', description: '' }, { label: 'Yes, but slowly (recommended)', description: 'after review' }, { label: 'No (Recommended)', description: '' }] }

test('a question without descriptions keeps one wrapping row of buttons', async ($, on) => {
  await start($, on, ok([asking({ toolUseId: 'tu1', questions: [{ ...SHIP, options: SHIP.options.map(o => ({ ...o, description: '' })) }], askedAt: 1, answerable: true })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const options = await ui.find({ key: 'local_a:q0:options' })
  expect([options?.props.flexDirection, options?.props.flexWrap]).toEqual(['row', 'wrap'])
  expect((options?.children as { props: { key?: string } }[]).map(c => c.props.key)).toEqual(['local_a:q0:o0:pick', 'local_a:q0:o1:pick', 'local_a:q0:o2:pick'])
  await ui.unmount()
})

test('a recommended option shows a star without its suffix, leads, and answers with its own label', async ($, on) => {
  const { otherRuns } = await start($, on, ok([asking({ toolUseId: 'tu1', questions: [SHIP], askedAt: 1, answerable: true })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const pick = async (oi: number) => {
    const { label, variant } = (await ui.find({ key: `local_a:q0:o${oi}:pick` }))?.props ?? {}
    return [label, variant]
  }
  expect(await pick(0)).toEqual(['Yes', undefined])
  expect(await pick(1)).toEqual(['★ Yes, but slowly', 'primary'])
  expect(await pick(2)).toEqual(['★ No', undefined])
  await ui.press({ key: 'local_a:q0:o2:pick' })
  const answers = otherRuns.filter(r => r.argv[2] === 'answer')
  expect(JSON.parse(answers[0]?.stdin ?? '')).toEqual({ 'Ship it?': 'No (Recommended)' })
  await ui.unmount()
})

test('pressing an option answers the worker with that pick', async ($, on) => {
  const toasts = collectToasts(on)
  const { otherRuns } = await start($, on, ok([asking({ toolUseId: 'tu1', questions: [DB], askedAt: 1, answerable: true })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:q0:o1:pick' })

  const answers = otherRuns.filter(r => r.argv[2] === 'answer')
  expect(answers.map(r => r.argv)).toEqual([['dev', 'desktop', 'answer', 'local_a', 'tu1']])
  expect(JSON.parse(answers[0]?.stdin ?? '')).toEqual({ 'Which DB?': 'Mongo' })
  expect(toasts).toEqual(['answered Worker A: Mongo'])
  // Sent: the options go until the worker's next poll drops the question
  expect(await ui.find({ key: 'local_a:q0:o0:pick' })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^sent: Mongo; waiting for the worker$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('a refused answer says why and keeps the options', async ($, on) => {
  const toasts = collectToasts(on)
  await start($, on, ok([asking({ toolUseId: 'tu1', questions: [DB], askedAt: 1, answerable: true })]), {
    answer: { exitCode: 1, stdout: '', stderr: 'local_a no longer waits on question tu1' },
  })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:q0:o0:pick' })
  expect(toasts).toEqual(['Worker A not answered: local_a no longer waits on question tu1'])
  expect(await ui.find({ key: 'local_a:q0:o0:pick' })).toBeTruthy()
  await ui.unmount()
})

test('a multi-select or several questions take toggles, then Send', async ($, on) => {
  const toasts = collectToasts(on)
  const { otherRuns } = await start($, on, ok([asking({ toolUseId: 'tu2', questions: [DB, FEATURES], askedAt: 1, answerable: true })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ key: 'local_a:q0:o0:pick' }))?.props.label).toBe('○ Postgres')
  expect((await ui.find({ key: 'local_a:q1:o0:pick' }))?.props.label).toBe('☐ Auth')
  // Toggles lead with what is picked, not the first option
  expect((await ui.find({ key: 'local_a:q0:o0:pick' }))?.props.variant).toBeUndefined()

  // Not every question picked: nothing sent
  await ui.press({ key: 'local_a:q1:o0:pick' })
  await ui.press({ key: 'local_a:answer' })
  expect(toasts).toEqual(['pick an answer to "Which DB?" first'])
  expect(otherRuns.filter(r => r.argv[2] === 'answer')).toEqual([])

  await ui.press({ key: 'local_a:q0:o0:pick' })
  await ui.press({ key: 'local_a:q0:o1:pick' })
  await ui.press({ key: 'local_a:q1:o1:pick' })
  expect((await ui.find({ key: 'local_a:q0:o1:pick' }))?.props.variant).toBe('primary')
  expect((await ui.find({ key: 'local_a:q0:o1:pick' }))?.props.label).toBe('◉ Mongo')
  expect((await ui.find({ key: 'local_a:q0:o0:pick' }))?.props.label).toBe('○ Postgres')
  expect((await ui.find({ key: 'local_a:q1:o0:pick' }))?.props.label).toBe('☑ Auth')
  await ui.press({ key: 'local_a:answer' })
  const sent = otherRuns.filter(r => r.argv[2] === 'answer')
  expect(sent.map(r => r.argv[4])).toEqual(['tu2'])
  expect(JSON.parse(sent[0]?.stdin ?? '')).toEqual({ 'Which DB?': 'Mongo', 'Which features?': 'Auth, Search' })
  await ui.unmount()
})

test('a question its mod does not watch: the options open its session, and say so', async ($, on) => {
  const { otherRuns } = await start($, on, ok([asking({ toolUseId: 'tu3', questions: [DB], askedAt: 0, answerable: false })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^answered in its session: an option opens it$/ }))?.text).toBeTruthy()
  await ui.press({ key: 'local_a:q0:o0:pick' })
  expect(otherRuns.map(r => r.argv)).toContainEqual(['open', 'claude://code/continue?session=local_a'])
  expect(otherRuns.filter(r => r.argv[2] === 'answer')).toEqual([])
  await ui.unmount()
})

test('picks for a question no longer asked go', async ($, on) => {
  let workers = [asking({ toolUseId: 'tu2', questions: [DB, FEATURES], askedAt: 1, answerable: true })]
  const { clock } = await start($, on, () => ok(workers))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:q0:o1:pick' })
  expect((await ui.find({ key: 'local_a:q0:o1:pick' }))?.props.label).toBe('◉ Mongo')

  workers = [asking({ toolUseId: 'tu4', questions: [DB, FEATURES], askedAt: 2, answerable: true })]
  await clock.advance(2000)
  expect((await ui.find({ key: 'local_a:q0:o1:pick' }))?.props.label).toBe('○ Mongo')
  await ui.unmount()
})

test('two quick picks both count, and a failed poll keeps them', async ($, on) => {
  let ran: Ran = ok([asking({ toolUseId: 'tu2', questions: [DB, FEATURES], askedAt: 1, answerable: true })])
  const { clock } = await start($, on, () => ran)
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await Promise.all([ui.press({ key: 'local_a:q1:o0:pick' }), ui.press({ key: 'local_a:q1:o1:pick' })])
  expect((await ui.find({ key: 'local_a:q1:o0:pick' }))?.props.label).toBe('☑ Auth')
  expect((await ui.find({ key: 'local_a:q1:o1:pick' }))?.props.label).toBe('☑ Search')

  ran = { exitCode: 1, stdout: '', stderr: 'boom' }
  await clock.advance(2000)
  ran = ok([asking({ toolUseId: 'tu2', questions: [DB, FEATURES], askedAt: 1, answerable: true })])
  await clock.advance(2000)
  expect((await ui.find({ key: 'local_a:q1:o1:pick' }))?.props.label).toBe('☑ Search')
  await ui.unmount()
})

test('a second press while the answer is written sends nothing more', async ($, on) => {
  const { otherRuns } = await start($, on, ok([asking({ toolUseId: 'tu1', questions: [DB], askedAt: 1, answerable: true })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await Promise.all([ui.press({ key: 'local_a:q0:o0:pick' }), ui.press({ key: 'local_a:q0:o1:pick' })])
  expect(otherRuns.filter(r => r.argv[2] === 'answer').length).toBe(1)
  await ui.unmount()
})

const FILE = { question: 'Which file should I describe?', header: '', multiSelect: false, options: [{ label: 'ask.ts', description: '' }, { label: 'register.tsx', description: '' }] }
const replying = (ask: Worker['ask']) => worker({ state: 'blocked', status: 'blocked', statusText: 'pick which file', ask })

test("a worker's wrap_up ask shows its reply buttons; a press asks the boss to message the worker that label", async ($, on) => {
  const texts = submits(on)
  const { otherRuns } = await start($, on, ok([replying({ toolUseId: 'w1', questions: [FILE], askedAt: 0, answerable: false, reply: true })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^Which file should I describe\?$/ }))?.text).toBeTruthy()
  expect(await ui.find({ type: 'Text', text: /pick which file/ })).toBeFalsy()
  expect(await ui.find({ type: 'Text', text: /opens it/ })).toBeFalsy()
  await ui.press({ key: 'local_a:q0:o1:pick' })

  expect(texts).toEqual(['dev-board: message Worker A (local_a): register.tsx'])
  expect(otherRuns.filter(r => r.argv[2] === 'answer' || r.argv[0] === 'open')).toEqual([])
  expect(await ui.find({ key: 'local_a:q0:o0:pick' })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^sent: register.tsx; waiting for the worker$/ }))?.text).toBeTruthy()
  // Its options gone, line 1 says the question
  expect(await header(ui)).toBe('Worker A Which file should I describe?')
  await ui.unmount()
})

test("a listening worker's reply goes to its reply file for its own mod, not through the boss", async ($, on) => {
  const texts = submits(on)
  const toasts = collectToasts(on)
  const written = new Map<string, string>()
  on('fs.write', (_, e) => {
    written.set(e.path, e.text)
    return { value: undefined }
  })
  const { otherRuns } = await start($, on, ok([replying({ toolUseId: 'w1', questions: [FILE], askedAt: 0, answerable: false, reply: true, listening: true })]), {}, { HOME: '/home' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ type: 'Text', text: /opens it/ })).toBeFalsy()
  await ui.press({ key: 'local_a:q0:o1:pick' })

  const path = '/home/.local/state/dev/desktop/questions/local_a.reply.json'
  expect(JSON.parse(written.get(`${path}.tmp`) ?? 'null')).toEqual({ toolUseId: 'w1', text: 'register.tsx', writtenAt: 0 })
  expect(otherRuns.map(r => r.argv)).toContainEqual(['mv', '-f', `${path}.tmp`, path])
  expect(texts).toEqual([])
  expect(otherRuns.filter(r => r.argv[0] === 'open')).toEqual([])
  expect(toasts).toEqual(['replied to Worker A: register.tsx'])
  expect((await ui.find({ type: 'Text', text: /^sent: register.tsx; waiting for the worker$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test("a reply the worker's mod has not taken 10 s on is offered again", async ($, on) => {
  const toasts = collectToasts(on)
  on('fs.write', () => ({ value: undefined }))
  const { clock } = await start($, on, ok([replying({ toolUseId: 'w1', questions: [FILE], askedAt: 0, answerable: false, reply: true, listening: true })]), {}, { HOME: '/home' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:q0:o1:pick' })
  await clock.advance(8000)
  expect(await ui.find({ key: 'local_a:q0:o0:pick' })).toBeFalsy()
  await clock.advance(2000)
  expect(toasts).toEqual(['replied to Worker A: register.tsx', 'Worker A did not take the reply: press it again'])
  expect(await ui.find({ key: 'local_a:q0:o0:pick' })).toBeTruthy()
  await ui.unmount()
})

test("a reply file not written says why and keeps the buttons, relaying nothing", async ($, on) => {
  const texts = submits(on)
  const toasts = collectToasts(on)
  on('fs.write', () => ({ value: undefined }))
  await start($, on, ok([replying({ toolUseId: 'w1', questions: [FILE], askedAt: 0, answerable: false, reply: true, listening: true })]), { mv: { exitCode: 1, stdout: '', stderr: 'EACCES' } }, { HOME: '/home' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:q0:o1:pick' })
  expect(toasts).toEqual(['reply for Worker A not sent: EACCES'])
  expect(texts).toEqual([])
  expect(await ui.find({ key: 'local_a:q0:o0:pick' })).toBeTruthy()
  await ui.unmount()
})

test('a refused reply keeps the buttons', async ($, on) => {
  submits(on, () => ({ drop: 'blocked by a hook' }))
  const toasts = collectToasts(on)
  await start($, on, ok([replying({ toolUseId: 'w1', questions: [FILE], askedAt: 0, answerable: false, reply: true })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:q0:o0:pick' })
  expect(toasts).toEqual(['answer for Worker A not sent: blocked by a hook'])
  expect(await ui.find({ key: 'local_a:q0:o0:pick' })).toBeTruthy()
  await ui.unmount()
})

test('a wrap_up ask without reply buttons shows the question, answered with Message…', async ($, on) => {
  await start($, on, ok([replying({ toolUseId: 'w1', questions: [{ ...FILE, options: [] }], askedAt: 0, answerable: false, reply: true })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^Which file should I describe\?$/ }))?.text).toBeTruthy()
  expect((await ui.find({ type: 'Text', text: /^answer it with Message…$/ }))?.text).toBeTruthy()
  expect((await ui.find({ key: 'local_a:message' }))?.props).toMatchObject({ plain: true, dimColor: false })
  await ui.unmount()
})

test('an idle worker asking a question keeps its own row', async ($, on) => {
  await start($, on, ok([worker({ state: 'idle', status: 'idle', ask: { toolUseId: 'w1', questions: [FILE], askedAt: 0, answerable: false, reply: true } })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ key: 'local_a:q0:o0:pick' })).toBeTruthy()
  await ui.unmount()
})

test('a dev printing a question this mod does not read says so', async ($, on) => {
  await start($, on, ok([{ ...asking(null), ask: { toolUseId: 'tu1' } as unknown as Worker['ask'] }]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /does not read/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('a message not sent opens its field again, holding its text, until one is sent', async ($, on) => {
  let refuse = true
  const texts = submits(on, text => (refuse ? { drop: 'blocked by a hook' } : { text }))
  const toasts = collectToasts(on)
  await start($, on, ok([worker({ state: 'busy', status: 'working' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await ui.press({ key: 'local_a:message' })
  await ui.input({ key: 'local_a:draft', text: 'use option B' })
  expect(toasts.at(-1)).toBe('message for Worker A not sent: blocked by a hook')
  expect((await ui.find({ key: 'local_a:draft' }))?.props.value).toBe('use option B')
  // Under the field, what to do; none before a refusal, nor once sent
  expect((await ui.find({ type: 'Text', text: /^not sent:/ }))?.props).toMatchObject({ color: 'warning', size: 'footnote' })
  expect((await ui.find({ type: 'Text', text: /^not sent:/ }))?.text).toBe('not sent: press Send to try again, or Cancel')
  // Not on the terminal, which has the toast only
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await terminal.find({ type: 'Text', text: /^not sent:/ })).toBeFalsy()
  await terminal.unmount()

  // Send, the person typing nothing more, sends it again
  refuse = false
  await ui.press({ key: 'local_a:send' })
  expect(await ui.find({ type: 'Text', text: /^not sent:/ })).toBeFalsy()
  expect(texts).toEqual(['dev-board: message Worker A (local_a): use option B', 'dev-board: message Worker A (local_a): use option B'])

  // Edited after a refusal, a redraw shows the edit, and Send sends it
  refuse = true
  await ui.press({ key: 'local_a:message' })
  await ui.input({ key: 'local_a:draft', text: 'use option B' })
  await ui.input({ key: 'local_a:draft', text: 'use option C', kind: 'change' })
  expect((await ui.find({ key: 'local_a:draft' }))?.props.value).toBe('use option C')
  refuse = false
  await ui.press({ key: 'local_a:send' })
  expect(texts.at(-1)).toBe('dev-board: message Worker A (local_a): use option C')
  expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
  await ui.press({ key: 'local_a:message' })
  expect((await ui.find({ key: 'local_a:draft' }))?.props.value).toBeUndefined()
  await ui.unmount()
})

test("a message not sent leaves another row's open field alone, its text kept for its own", async ($, on) => {
  let release = () => {}
  on('prompt.submit', () => new Promise(resolve => (release = () => resolve({ drop: 'blocked by a hook' }))))
  await start($, on, ok([worker({ state: 'busy', status: 'working' }), worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'working' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await unfold(ui, 'local_b')
  await ui.press({ key: 'local_a:message' })
  const sent = ui.input({ key: 'local_a:draft', text: 'use option B' })
  await new Promise(resolve => setTimeout(resolve, 0))
  await ui.press({ key: 'local_b:message' })
  release()
  await sent
  expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
  expect(await ui.find({ key: 'local_b:draft' })).toBeTruthy()
  await ui.press({ key: 'local_a:message' })
  expect((await ui.find({ key: 'local_a:draft' }))?.props.value).toBe('use option B')
  await ui.unmount()
})

test('a working row keeps a status on line 1 between tools', async ($, on) => {
  let tool = 'Bash'
  const { clock } = await start($, on, () => ok([worker({ state: 'busy', status: 'working', statusText: tool, tool })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^Bash$/ }))?.text).toBeTruthy()
  tool = ''
  await clock.advance(2000)
  expect((await ui.find({ type: 'Text', text: /^working$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('reply lines drop turn-end markers and show a link as its text', async ($, on) => {
  await start($, on, ok([
    worker({ status: 'idle', lastLines: ['See [RESULT.md](RESULT.md), ![shot](a.png), [Foo](https://w/Foo_(bar)) and `[a](b)`.', '**IDLE**'] }),
    worker({ id: 'local_b', title: 'Worker B', status: 'waiting', statusText: 'CI on PR #80', lastLines: ['Pushed.', 'WAITING (CI on PR #80)'] }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  await unfold(ui, 'local_b')
  // Each detail ends in its reply lines
  expect(await textsOf(ui, 'local_b:detail')).toEqual(['feature/a', 'Pushed.'])
  expect(await textsOf(ui, 'local_a:detail')).toEqual(['feature/a', 'See RESULT.md, shot, Foo and [a](b).'])
  // Muted, not italic: italic grey at that size reads as noise
  expect((await ui.findAll({ type: 'Text', text: 'Pushed.' })).map(t => [t.props.color, t.props.dimColor, t.props.italic])).toEqual([['subtle', undefined, undefined]])
  expect((await ui.findAll({ type: 'Text', text: /IDLE|WAITING/ })).map(t => t.text)).toEqual(['WAITING', 'IDLE'])
  await ui.unmount()
})

test('a reply line asking the question the row shows is not said again', async ($, on) => {
  await start($, on, ok([{ ...asking({ toolUseId: 'tu1', questions: [DB], askedAt: 1, answerable: true }), lastLines: ['I need a pick.', '**Which DB?**'] }]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  expect((await ui.findAll({ type: 'Text', text: /Which DB/ })).map(t => t.text)).toEqual(['Which DB?'])
  expect((await ui.find({ type: 'Text', text: /^I need a pick\.$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('the meta line: model·effort, context use, each PR, the task progress, split by ·; the branch only in the detail', async ($, on) => {
  const branch = 'feature/board-test-pass-801d74'
  await start($, on, ok([worker({ state: 'busy', status: 'working', model: 'opus', effort: 'medium', context: 16, branch, prs: [OPEN_PR], plan: [...PLAN] })]), { gh: checksOf('pass') })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ key: 'local_a:meta' }))?.props).toMatchObject({ flexDirection: 'row', flexWrap: 'wrap', minWidth: 0 })
  expect(await prRow(ui, 'local_a:meta')).toEqual(['opus·medium', '·', '16%', '·', '[PR #80 open]', 'PR #80', '[CI passed]', 'c0', '·', '1/3'])
  expect((await ui.find({ type: 'Text', text: '16%' }))?.props).toMatchObject({ size: 'footnote', color: 'subtle', tabular: true })
  expect(await ui.find({ type: 'Text', text: branch })).toBeFalsy()
  await unfold(ui)
  // The branch cut at its start, its unique end kept, in the mono face
  expect((await ui.find({ type: 'Text', text: branch }))?.props).toMatchObject({ mono: true, wrap: 'truncate-start' })
  await ui.unmount()
})

test('the meta line leaves out what a worker lacks: no effort, the model alone; nothing, no line', async ($, on) => {
  await start($, on, ok([
    worker({ state: 'busy', status: 'working', statusText: 'Bash', model: 'haiku', effort: '', context: 9 }),
    worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'working', model: 'claude-x-1', effort: 'high', context: null }),
    // A dev older than this mod: no model
    worker({ id: 'local_c', title: 'Worker C', state: 'busy', status: 'working', context: 5 }),
    worker({ id: 'local_d', title: 'Worker D', state: 'busy', status: 'working' }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await prRow(ui, 'local_a:meta')).toEqual(['haiku', '·', '9%'])
  expect(await prRow(ui, 'local_b:meta')).toEqual(['claude-x-1·high'])
  expect(await prRow(ui, 'local_c:meta')).toEqual(['5%'])
  expect(await ui.find({ key: 'local_d:meta' })).toBeFalsy()
  await ui.unmount()
})

test('a named worker is titled by its name alone, its title in the detail; one without a name by its title', async ($, on) => {
  await start($, on, ok([
    worker({ name: 'fix-login', title: 'Fix the login redirect loop', status: 'working', state: 'busy' }),
    worker({ id: 'local_b', title: 'Worker B', status: 'working', state: 'busy' }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await header(ui)).toBe('fix-login working')
  expect(await header(ui, 'local_b')).toBe('Worker B working')
  expect(await ui.find({ type: 'Text', text: /Fix the login/ })).toBeFalsy()
  // Its title, unlike its name, shows in the detail
  await unfold(ui)
  expect((await ui.find({ type: 'Text', text: /Fix the login/ }))?.text).toBe('Fix the login redirect loop')
  await ui.unmount()
})

test('colour only on the dot, the section header and a Needs you line; no borders', async ($, on) => {
  await start($, on, ok(BY_STATUS.map(w => ({ ...w, statusText: 'on it' }))))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.findAll({ type: 'Box' })).filter(b => b.props.borderStyle !== undefined)).toEqual([])
  // The dot says the section, not the app's state: a busy worker under Needs you is red; a stopped one muted as an idle one (inactive on the terminal)
  const dot = async (id: string, key = `${id}:row`) => ((await ui.find({ key }))?.children[0] as { props: Record<string, unknown> } | undefined)?.props.color
  expect(await Promise.all(['local_n', 'local_w', 'local_b', 'local_k', 'local_t'].map(id => dot(id)))).toEqual(['error', 'warning', 'warning', 'success', 'success'])
  await ui.press({ key: 'idle:fold' })
  expect(await Promise.all(['local_i', 'local_s'].map(id => dot(id, `${id}:header`)))).toEqual(['subtle', 'subtle'])
  // What line 1 says: red under Needs you, else muted
  const says = async (id: string) => {
    const cell = (await ui.find({ key: `${id}:header` }))?.children[1] as { children: { props: Record<string, unknown> }[] } | undefined
    const text = cell?.children[0]?.props
    return [text?.size, text?.color, text?.weight]
  }
  expect(await Promise.all(['local_n', 'local_w', 'local_k'].map(says))).toEqual([
    ['footnote', 'error', 'semibold'],
    ['footnote', 'subtle', 'regular'],
    ['footnote', 'subtle', 'regular'],
  ])
  await ui.unmount()
})

test('a question sits under line 1, before the meta line', async ($, on) => {
  const ask = { toolUseId: 'tu1', askedAt: 1, answerable: true, questions: [{ header: '', question: 'Ship it?', multiSelect: false, options: [{ label: 'Yes', description: '' }] }] }
  await start($, on, ok([worker({ status: 'needs you', context: 5, ask })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const rows = await ui.find({ key: 'local_a:rows' })
  expect((rows?.children as { props: { key?: string } }[]).map(c => c.props.key)).toEqual(['local_a:header', 'local_a:ask', 'local_a:meta'])
  await ui.unmount()
})

test("the pane's title carries the counts while it is open, retitled only when they change", async ($, on) => {
  const opens: unknown[] = []
  let panes = [{ id: 'board', title: 'Workers', isShown: true, isFocused: false, isPlaced: true }]
  on('ui.open', (_, e) => {
    opens.push(e)
    panes = panes.map(p => ({ ...p, title: e.title ?? p.title }))
    return { value: { isPlaced: true } }
  })
  let now: Ran = ok(WORKERS)
  const { clock } = await start($, on, () => now, {}, {}, () => panes)
  await clock.settle()
  expect(opens).toEqual([{ id: 'board', title: 'Workers · 3' }])
  // A poll with the same counts retitles nothing
  now = ok(WORKERS.map(w => ({ ...w, since: '9m' })))
  await clock.advance(2000)
  expect(opens.length).toBe(1)
  now = ok([...WORKERS, worker({ id: 'local_n', status: 'needs you' })])
  await clock.advance(2000)
  expect(opens.at(-1)).toEqual({ id: 'board', title: 'Workers · 4 · 1 needs you' })
  // /board opens it with them
  await runBoard($)
  expect(opens.at(-1)).toEqual({ id: 'board', title: 'Workers · 4 · 1 needs you' })
  // A failed poll keeps the last
  const opened = opens.length
  now = { exitCode: 1, stdout: '', stderr: 'boom' }
  await clock.advance(2000)
  expect(opens.length).toBe(opened)
  // Down again: the needs you part goes, then the count
  now = ok(WORKERS)
  await clock.advance(2000)
  expect(opens.at(-1)).toEqual({ id: 'board', title: 'Workers · 3' })
  now = ok([])
  await clock.advance(2000)
  expect(opens.at(-1)).toEqual({ id: 'board', title: 'Workers' })
})

test('a failed retitle says so once', async ($, on) => {
  const opens: string[] = []
  on('ui.open', (_, e) => {
    opens.push(e.title ?? '')
    return { deny: 'no surface' }
  })
  const toasts = collectToasts(on)
  const { clock } = await start($, on, ok(WORKERS), {}, {}, () => [{ id: 'board', title: 'Workers', isShown: true, isFocused: false, isPlaced: true }])
  await clock.advance(4000)
  expect(opens).toEqual(['Workers · 3'])
  const failed = toasts.filter(t => t.startsWith('pane not retitled'))
  expect(failed.length).toBe(1)
  expect(failed[0]).toMatch(/no surface/)
})

test('a pane not open is not opened to retitle it, nor looked for every tick; one /board opens is looked at again', async ($, on) => {
  const opens: unknown[] = []
  on('ui.open', (_, e) => {
    opens.push(e)
    return { value: { isPlaced: true } }
  })
  let panes: UiPane[] = []
  let panesRead = 0
  const { clock } = await start($, on, ok(WORKERS), {}, {}, () => {
    panesRead += 1
    return panes
  })
  await clock.advance(4000)
  expect(opens).toEqual([])
  // Looked at once per title, not every tick
  expect(panesRead).toBe(1)
  // /board opens it (here with older counts, as when a poll lands between its read and its open): the next tick looks again
  panes = [{ id: 'board', title: 'Workers', isShown: true, isFocused: false, isPlaced: true }]
  await runBoard($)
  await clock.advance(2000)
  expect(panesRead).toBe(2)
  expect(opens.at(-1)).toEqual({ id: 'board', title: 'Workers · 3' })
})

test('a thin rule parts the rows of a section: a faint line on the desktop, a blank row on the terminal', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working' }), worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'working' })]))
  const desktop = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await desktop.find({ key: 'Working:rule0' })).toBeFalsy()
  const line = await desktop.find({ key: 'Working:rule1' })
  expect(line?.props.height).toBe(1)
  expect((line?.children[0] as { props: Record<string, unknown> } | undefined)?.props.backgroundColor).toMatch(/^rgba\(/)
  await desktop.unmount()
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  const blank = await terminal.find({ key: 'Working:rule1' })
  expect([blank?.props.height, blank?.children.length]).toEqual([1, 0])
  await terminal.unmount()
})

const todoRan = (items: TodoItem[], minutesAgo = 12, stale = false): Ran => ({ exitCode: 0, stdout: JSON.stringify({ items, updated: -minutesAgo * 60, stale }), stderr: '' })
const TODO: TodoItem[] = [
  { id: 1, text: 'Answer Vince in Slack', done: false },
  { id: 5, text: 'Merge PR #87', link: 'https://github.com/o/r/pull/87', done: false },
  { id: 4, text: 'Staging access for lw', on: 'Marek (Ops)', done: false },
]

const VIEW_OPEN: Ran = { exitCode: 0, stdout: '{"state":"OPEN"}', stderr: '' }

/** The to do block as read, a line per row: its Texts and icons, space-joined; undefined with no block. */
async function todoRows(ui: Mounted) {
  const block = await ui.find({ key: 'todo' })
  return block === undefined ? undefined : (block.children as El[]).map(row => readEl(row).join(' '))
}

test('the to do list shows above the sections: header, then a row per item, on every surface', async ($, on) => {
  const { todoRuns, clock } = await start($, on, ok(WORKERS), { todo: todoRan(TODO), gh: VIEW_OPEN })
  expect(todoRuns[0]).toEqual(['dev', 'todo', '--json', '--boss', 'local_boss'])
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    const pr = surface === 'desktop' ? '[PR #87 open]' : '○'
    expect(await todoRows(ui)).toEqual(['TO DO 3 · 1 on others updated 12m', '☐ Answer Vince in Slack', `☐ Merge PR #87 ${pr} #87`, '◌ Staging access for lw on Marek (Ops)'])
    // Above the worker sections
    const texts = (await ui.findAll({ type: 'Text' })).map(t => t.text)
    expect(texts.indexOf('Answer Vince in Slack')).toBeLessThan(texts.indexOf('WORKING'))
    await ui.unmount()
  }
  // Its age follows the clock
  await clock.advance(60_000)
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await todoRows(ui))?.[0]).toBe('TO DO 3 · 1 on others updated 13m')
  await ui.unmount()
})

test("a to do PR link's icon follows the PR's state, none before it is read or when gh fails; read once a minute, dropped with the link", async ($, on) => {
  let state: Ran = { exitCode: 0, stdout: '{"state":"MERGED"}', stderr: '' }
  const { otherRuns, clock } = await start($, on, ok(WORKERS), { todo: todoRan([TODO[1] as TodoItem]), gh: () => state })
  const views = () => otherRuns.filter(r => r.argv[1] === 'pr' && r.argv[2] === 'view')
  expect(views()[0]?.argv).toEqual(['gh', 'pr', 'view', 'https://github.com/o/r/pull/87', '--json', 'state'])
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await todoRows(ui))?.[1]).toBe('☐ Merge PR #87 [PR #87 merged] #87')
  const before = views().length
  await clock.advance(30_000)
  expect(views().length).toBe(before)
  expect(views().every(r => r.argv[3] === 'https://github.com/o/r/pull/87')).toBe(true)
  // Failing gh: the last good state is replaced by none
  state = { exitCode: 1, stdout: '', stderr: 'no gh' }
  await clock.advance(60_000)
  expect((await todoRows(ui))?.[1]).toBe('☐ Merge PR #87 #87')
  await ui.unmount()
})

test('a to do PR link shows no state icon until gh has answered', async ($, on) => {
  await start($, on, ok(WORKERS), { todo: todoRan([TODO[1] as TodoItem]), gh: { exitCode: 1, stdout: '', stderr: 'boom' } })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await todoRows(ui))?.[1]).toBe('☐ Merge PR #87 #87')
  await ui.unmount()
})

test('an item waiting on someone else: a dotted circle, its text quieter, who at the right in amber', async ($, on) => {
  await start($, on, ok(WORKERS), { todo: todoRan([TODO[2] as TodoItem]) })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await todoRows(ui)).toEqual(['TO DO 1 · 1 on others updated 12m', '◌ Staging access for lw on Marek (Ops)'])
  expect((await ui.find({ type: 'Text', text: 'Staging access for lw' }))?.props.color).toBe('subtle')
  expect((await ui.find({ type: 'Text', text: 'on Marek (Ops)' }))?.props.color).toBe('warning')
  await ui.unmount()
})

test("an item's link opens as the board's PR links do: a PR its icon and number, another link ↗", async ($, on) => {
  const other = { id: 2, text: 'Read the thread', link: 'https://example.slack.com/archives/C1/p2', done: false }
  await start($, on, ok(WORKERS), { todo: todoRan([TODO[1] as TodoItem, other]), gh: VIEW_OPEN })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await todoRows(ui)).toEqual(['TO DO 2 updated 12m', '☐ Merge PR #87 [PR #87 open] #87', '☐ Read the thread ↗'])
  expect((await ui.findAll({ type: 'Link' })).filter(l => /87|slack/.test(String(l.props.href))).map(l => [l.props.label, l.props.href])).toEqual([
    ['#87', 'https://github.com/o/r/pull/87'],
    ['↗', 'https://example.slack.com/archives/C1/p2'],
  ])
  await ui.unmount()
})

test('nine items: the first five, then a toggle that opens the rest and folds them again', async ($, on) => {
  const nine = Array.from({ length: 9 }, (_, i) => ({ id: i + 1, text: `Item ${i + 1}`, done: false }))
  await start($, on, ok(WORKERS), { todo: todoRan(nine) })
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect(await todoRows(ui)).toEqual(['TO DO 9 updated 12m', '☐ Item 1', '☐ Item 2', '☐ Item 3', '☐ Item 4', '☐ Item 5', '▸ 4 more'])
    expect(await ui.find({ key: 'todo:head:fold' })).toBeFalsy()
    expect((await ui.find({ key: 'todo:fold' }))?.props).toMatchObject({ plain: true, dimColor: true })
    await ui.press({ key: 'todo:fold' })
    expect((await todoRows(ui))?.slice(0, 2)).toEqual(['▾ TO DO 9 updated 12m', '☐ Item 1'])
    expect((await todoRows(ui))?.slice(-2)).toEqual(['☐ Item 9', '▾ show fewer'])
    // The open header folds it too
    await ui.press({ key: 'todo:head:fold' })
    expect((await todoRows(ui))?.at(-1)).toBe('▸ 4 more')
    await ui.unmount()
  }
})

test('a list of five or fewer has no toggle', async ($, on) => {
  const five = Array.from({ length: 5 }, (_, i) => ({ id: i + 1, text: `Item ${i + 1}`, done: false }))
  await start($, on, ok(WORKERS), { todo: todoRan(five) })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ key: 'todo:fold' })).toBeFalsy()
  expect(await ui.find({ key: 'todo:head:fold' })).toBeFalsy()
  await ui.unmount()
})

test('a stale list dims its age on the terminal; a fresh one does not; the desktop keeps it muted, the age saying it', async ($, on) => {
  let isStale = false
  const { clock } = await start($, on, ok(WORKERS), { todo: () => todoRan(TODO, 75, isStale) })
  const fresh = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await fresh.find({ type: 'Text', text: /^updated/ }))?.props.dimColor).toBe(false)
  await fresh.unmount()
  isStale = true
  await clock.advance(2000)
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    const age = await ui.find({ type: 'Text', text: /^updated/ })
    expect([age?.text, age?.props.color, age?.props.dimColor]).toEqual(['updated 1h 15m', 'subtle', surface === 'terminal'])
    await ui.unmount()
  }
})

test('a ticked item shows ticked and struck through while the list still carries it, not counted', async ($, on) => {
  await start($, on, ok(WORKERS), { todo: todoRan([{ ...(TODO[0] as TodoItem), done: true }, TODO[2] as TodoItem]) })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await todoRows(ui)).toEqual(['TO DO 1 · 1 on others updated 12m', '☑ Answer Vince in Slack', '◌ Staging access for lw on Marek (Ops)'])
  const text = await ui.find({ type: 'Text', text: 'Answer Vince in Slack' })
  expect([text?.props.strikethrough, text?.props.color]).toEqual([true, 'subtle'])
  expect((await ui.find({ type: 'Text', text: '☑' }))?.props.color).toBe('success')
  await ui.unmount()
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect((await terminal.find({ type: 'Text', text: 'Answer Vince in Slack' }))?.props.color).toBe('inactive')
  await terminal.unmount()
})

/** The pane's Texts with no to do list read. */
async function paneWithoutTodo($: Engine, on: On, todo: Others['todo']) {
  const started = await start($, on, ok(WORKERS), { todo })
  await started.clock.advance(10_000)
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const texts = (await ui.findAll({ type: 'Text' })).map(t => t.text)
  expect(await ui.find({ key: 'todo' })).toBeUndefined()
  await ui.unmount()
  return { texts, ...started }
}

const BOARD_TEXTS = ['WORKING', '1', '●', 'Worker A', 'Bash', '42%', 'WAITING', '1', '●', 'Worker B', 'blocked · pick one', 'PR #78', 'no checks reported', 'IDLE', '1', '●', 'Worker C']

test('an empty list shows no block: the board as without one', async ($, on) => {
  const { texts, todoRuns } = await paneWithoutTodo($, on, todoRan([]))
  expect(todoRuns.length).toBe(6)
  expect(texts.filter(t => t !== '')).toEqual(BOARD_TEXTS)
})

for (const [why, todo] of [
  ['a dev without dev todo', NO_TODO],
  ['a worker session dev todo refuses', { exitCode: 1, stdout: '', stderr: '❌ local_boss is a worker another session started, not a boss' }],
  ['dev todo that cannot start', 'spawn dev ENOENT'],
  ['dev todo printing what this mod does not read', { exitCode: 0, stdout: '{"todo":[]}', stderr: '' }],
] as const) {
  test(`${why}: no block, the board intact, no toast, one debug line however many polls`, async ($, on) => {
    const toasts = collectToasts(on)
    const logs: string[] = []
    on('ui.log', (_, e) => {
      logs.push(`${e.to}: ${e.text}`)
      return { value: undefined }
    })
    const { texts, todoRuns } = await paneWithoutTodo($, on, todo)
    expect(todoRuns.length).toBe(6)
    expect(texts.filter(t => t !== '')).toEqual(BOARD_TEXTS)
    expect(toasts).toEqual([])
    const todoLogs = logs.filter(l => l.includes('to do'))
    expect(todoLogs.length).toBe(1)
    expect(todoLogs[0]).toMatch(/^debug: dev-board: no to do list: /)
  })
}

test('a list emptied after it showed takes its block away', async ($, on) => {
  let items = TODO
  const { clock } = await start($, on, ok(WORKERS), { todo: () => todoRan(items) })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ key: 'todo' })).toBeTruthy()
  items = []
  await clock.advance(2000)
  expect(await ui.find({ key: 'todo' })).toBeUndefined()
  await ui.unmount()
})

test('a GitLab merge request or a PR link with a path past its number shows as a PR; only GitHub links get a state icon', async ($, on) => {
  const links = ['https://gitlab.com/g/r/-/merge_requests/12', 'https://github.com/o/r/pull/87/files', 'https://github.com/o/r/pull/88#issuecomment-1', 'https://github.com/o/r/pulls']
  await start($, on, ok(WORKERS), { todo: todoRan(links.map((link, i) => ({ id: i + 1, text: `L${i + 1}`, link, done: false }))), gh: VIEW_OPEN })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await todoRows(ui))?.slice(1)).toEqual(['☐ L1 #12', '☐ L2 [PR #87 open] #87', '☐ L3 [PR #88 open] #88', '☐ L4 ↗'])
  await ui.unmount()
})

test('a read that fails after the list showed takes the block away until one succeeds; a new cause, or one back after a good read, is logged once more', async ($, on) => {
  const logs: string[] = []
  on('ui.log', (_, e) => {
    if (e.text.includes('to do')) logs.push(e.text)
    return { value: undefined }
  })
  let answer: Ran = todoRan(TODO)
  const { clock } = await start($, on, ok(WORKERS), { todo: () => answer })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ key: 'todo' })).toBeTruthy()
  answer = { exitCode: 1, stdout: '', stderr: 'boom' }
  await clock.advance(4000)
  expect(await ui.find({ key: 'todo' })).toBeUndefined()
  answer = todoRan(TODO)
  await clock.advance(2000)
  expect(await ui.find({ key: 'todo' })).toBeTruthy()
  answer = NO_TODO
  await clock.advance(4000)
  // The same cause after a good read is said again
  answer = todoRan(TODO)
  await clock.advance(2000)
  answer = NO_TODO
  await clock.advance(4000)
  expect(logs).toEqual(['dev-board: no to do list: boom', 'dev-board: no to do list: ❌ unknown flag: --json', 'dev-board: no to do list: ❌ unknown flag: --json'])
  await ui.unmount()
})

test('a list with an item this mod does not read shows no block', async ($, on) => {
  await start($, on, ok(WORKERS), { todo: todoRan([{ ...(TODO[0] as TodoItem), link: 87 as unknown as string }]) })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ key: 'todo' })).toBeUndefined()
  expect((await ui.find({ type: 'Text', text: 'Worker A' }))?.text).toBeTruthy()
  await ui.unmount()
})

test("a to do item on a worker on the board shows the worker's dot and state, its name opening the worker's row", async ($, on) => {
  const workers = [
    worker({ id: 'local_c', name: 'companion-1000', state: 'busy', status: 'working' }),
    worker({ id: 'local_i', name: 'docs', status: 'idle' }),
    worker({ id: 'local_j', name: 'other', status: 'idle' }),
  ]
  const items: TodoItem[] = [
    { id: 1, text: 'Rework companion 10:00', on: ' Companion-1000', done: false },
    { id: 2, text: 'Read the docs PR', on: 'docs', done: false },
    { id: 3, text: 'Staging access for lw', on: 'Marek (Ops)', done: false },
    { id: 4, text: 'Approve the deploy', done: false },
  ]
  await start($, on, ok(workers), { todo: todoRan(items) })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await todoRows(ui)).toEqual([
    'TO DO 4 · 3 on others updated 12m',
    '◌ Rework companion 10:00 ● companion-1000 · working',
    '◌ Read the docs PR ● docs · idle',
    '◌ Staging access for lw on Marek (Ops)',
    '☐ Approve the deploy',
  ])
  const dots = ((await ui.findAll({ type: 'Text', text: '●' })) as { props: Record<string, unknown> }[]).map(el => el.props.color)
  expect(dots.slice(0, 2)).toEqual(['success', 'subtle'])
  await ui.press({ key: 'todo:item1:owner' })
  expect(await ui.find({ key: 'local_c:detail' })).toBeTruthy()
  // An idle worker in the shared idle line gets its row, open
  expect(await ui.find({ key: 'local_i:row' })).toBeFalsy()
  await ui.press({ key: 'todo:item2:owner' })
  expect(await ui.find({ key: 'local_i:detail' })).toBeTruthy()
  await ui.unmount()
})

test('a ticked item on a worker shows ticked, with no live state', async ($, on) => {
  await start($, on, ok([worker({ name: 'companion-1000', state: 'busy', status: 'working' })]), { todo: todoRan([{ id: 1, text: 'Rework companion', on: 'companion-1000', done: true }]) })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await todoRows(ui))?.[1]).toBe('☑ Rework companion on companion-1000')
  await ui.unmount()
})

/** An assistant message calling one tool; `isError` for a call that failed. */
const called = (tool: string, input: Record<string, unknown>, isError = false) => ({ role: 'assistant' as const, text: '', toolUses: [{ tool_use_id: tool, tool, input, ...(isError ? { isError: true as const } : {}) }] })
const PLAN_ITEMS = [
  { content: 'Read the code', status: 'in_progress' },
  { content: 'Write the fix', status: 'pending' },
  { content: 'Run the tests', status: 'pending' },
]

/** Starts a boss whose transcript is `messages()`, with tool calls answered; the pane's boss line as read, undefined with none. */
async function startBoss($: Engine, on: On, messages: () => unknown[], panes: () => UiPane[] = () => []) {
  on('session.messages', () => ({ value: messages() as never }))
  on('tool.call', () => ({ result: {} }))
  const started = await start($, on, ok(WORKERS), { todo: todoRan(TODO) }, {}, panes)
  const line = async () => {
    const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
    const el = await ui.find({ key: 'boss' })
    const read = el === undefined ? undefined : readEl(el as El).join(' ')
    const keys = ((await ui.findAll({})) as { key?: string }[]).map(e => e.key)
    await ui.unmount()
    return { read, isAboveTodo: el === undefined || keys.indexOf('boss') < keys.indexOf('todo') }
  }
  return { ...started, line }
}

test("the boss's own line tops the board: its chapter, then the step its task list is on", async ($, on) => {
  const { line } = await startBoss($, on, () => [
    called('mcp__ccd_session__mark_chapter', { title: 'Old chapter' }),
    called('mcp__ccd_session__mark_chapter', { title: 'Board owner state' }),
    called('mcp__ccd_turn__update_plan', { items: PLAN_ITEMS, scope: 'first_task' }),
    called('mcp__ccd_turn__update_plan', { advance: true }),
    // Refused: changes nothing
    called('mcp__ccd_turn__update_plan', { advance: true }, true),
  ])
  expect(await line()).toEqual({ read: 'Board owner state ◐ Write the fix', isAboveTodo: true })
})

for (const [why, messages, read] of [
  ['done and start move the current step', [called('mcp__ccd_turn__update_plan', { items: PLAN_ITEMS }), called('mcp__ccd_turn__update_plan', { done: [1], start: 3 })], '◐ Run the tests'],
  ['wrap_up checks the list off: the chapter alone', [called('mcp__ccd_session__mark_chapter', { title: 'Fix' }), called('mcp__ccd_turn__update_plan', { items: PLAN_ITEMS }), called('mcp__ccd_turn__wrap_up', { recap: 'done' })], 'Fix'],
  ['a new items call replaces the list', [called('mcp__ccd_turn__update_plan', { items: PLAN_ITEMS }), called('mcp__ccd_turn__update_plan', { items: [{ content: 'Next', status: 'in_progress' }] })], '◐ Next'],
  ['advance past the last step leaves none current', [called('mcp__ccd_turn__update_plan', { items: [{ content: 'Only', status: 'in_progress' }] }), called('mcp__ccd_turn__update_plan', { advance: true })], undefined],
  ['no chapter and no list: no line', [], undefined],
] as const) {
  test(`the boss line: ${why}`, async ($, on) => {
    const { line } = await startBoss($, on, () => [...messages])
    expect((await line()).read).toBe(read)
  })
}

test("with its board open, the boss line follows the boss's own task list and chapter calls, not its subagents'; closed, /board reads it", async ($, on) => {
  let messages: unknown[] = []
  let panes: UiPane[] = []
  on('ui.open', () => ({ value: { isPlaced: true } }))
  submits(on)
  collectToasts(on)
  const { line, clock } = await startBoss($, on, () => messages, () => panes)
  expect((await line()).read).toBeUndefined()
  messages = [called('mcp__ccd_session__mark_chapter', { title: 'Closed' })]
  await $.tool.call({ tool: 'mcp__ccd_session__mark_chapter', title: 'Closed' })
  await clock.settle()
  expect((await line()).read).toBeUndefined()
  expect(await runBoard($)).toBeTruthy()
  await clock.settle()
  expect((await line()).read).toBe('Closed')
  panes = [{ id: 'board', title: 'Workers', isShown: true, isFocused: false, isPlaced: true }]
  messages = [...messages, called('mcp__ccd_turn__update_plan', { items: PLAN_ITEMS })]
  await $.tool.call({ tool: 'mcp__ccd_turn__update_plan', items: PLAN_ITEMS })
  await clock.settle()
  expect((await line()).read).toBe('Closed ◐ Read the code')
  // Its newest messages only: the chapter and the list set before them stay; a wrap_up ends the list
  messages = [called('mcp__ccd_turn__update_plan', { advance: true }, true)]
  await $.tool.call({ tool: 'mcp__ccd_turn__update_plan', advance: true })
  await clock.settle()
  expect((await line()).read).toBe('Closed ◐ Read the code')
  messages = [called('mcp__ccd_turn__wrap_up', { recap: 'done' })]
  await $.tool.call({ tool: 'mcp__ccd_turn__wrap_up', recap: 'done' })
  await clock.settle()
  expect((await line()).read).toBe('Closed')
  messages = [called('mcp__ccd_turn__update_plan', { items: PLAN_ITEMS })]
  await $.tool.call({ tool: 'mcp__ccd_turn__update_plan', agentId: 'a1', items: PLAN_ITEMS })
  await clock.settle()
  expect((await line()).read).toBe('Closed')
  await $.tool.call({ tool: 'mcp__ccd_turn__update_plan', items: PLAN_ITEMS })
  await clock.settle()
  expect((await line()).read).toBe('Closed ◐ Read the code')
  messages = [...messages, called('mcp__ccd_session__mark_chapter', { title: 'Verify' })]
  await $.tool.call({ tool: 'mcp__ccd_session__mark_chapter', title: 'Verify' })
  await clock.settle()
  expect((await line()).read).toBe('Verify ◐ Read the code')
})

/** A board with every part the desktop draws: each section, PRs of each state with CI, a task list, notes, a need, a question, the to do list. */
async function startFull($: Engine, on: On) {
  const prs = [OPEN_PR, { number: 7, url: 'https://github.com/o/r/pull/7', state: 'MERGED' }, { number: 9, url: 'https://github.com/o/r/pull/9', state: 'CLOSED' }]
  const workers = [
    ...BY_STATUS.map(w => ({ ...w, name: w.title.toLowerCase(), since: '41m', model: 'opus', effort: 'high', context: 40, plan: [...PLAN], prs, lastLines: ['Done.'], recap: 'All done.' })),
    worker({ id: 'local_q', name: 'asker', state: 'busy', status: 'needs you', ask: { toolUseId: 'tu1', questions: [DB, FEATURES], askedAt: 1, answerable: true } }),
    worker({ id: 'local_o', name: 'noted', status: 'idle', needsAction: 'reply yes', notes: [STUCK], branch: 'feature/very/long/branch-name' }),
  ]
  on('session.messages', () => ({ value: [called('mcp__ccd_session__mark_chapter', { title: 'Board owner state' }), called('mcp__ccd_turn__update_plan', { items: PLAN_ITEMS })] as never }))
  await start($, on, ok(workers), { gh: CHECKS, delivered: DELIVERED, todo: todoRan([...TODO, { id: 9, text: 'Pick a DB', on: 'asker', done: false }], 75, true) })
}

/** Opens everything a row can show: the idle line, each detail, a message field. */
async function openAll(ui: Mounted) {
  await ui.press({ key: 'idle:fold' })
  for (const b of await ui.findAll({ type: 'Button' })) if (b.key?.endsWith(':fold') && b.key !== 'idle:fold') await unfold(ui, b.key.replace(/:fold$/, ''))
  await ui.press({ key: 'local_k:message' })
}

test('the desktop draws from its tokens only: five colors, two sizes, two weights, its spacing steps, no dim text', async ($, on) => {
  await startFull($, on)
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await openAll(ui)
  const all = await ui.findAll({})
  const used = (type: string, prop: string) => new Set(all.filter(el => el.type === type).map(el => el.props[prop]).filter(v => v !== undefined))
  expect([...used('Text', 'color')].sort()).toEqual(['error', 'subtle', 'success', 'text', 'warning'])
  expect([...used('Text', 'size')].sort()).toEqual(['footnote'])
  expect([...used('Text', 'weight')].sort()).toEqual(['regular', 'semibold'])
  expect([...used('Text', 'dimColor')]).toEqual([false])
  // A dim Button (muted at rest, full under the pointer) only on the quiet idle line: its toggle and names
  const dimButtons = all.filter(el => el.type === 'Button' && el.props.dimColor === true).map(el => el.key)
  expect(dimButtons.every(key => key === 'idle:fold' || ['local_i:fold', 'local_s:fold'].includes(key ?? ''))).toBe(true)
  for (const prop of ['gap', 'rowGap', 'columnGap', 'paddingY']) {
    expect([...used('Box', prop)].every(v => [1, 'xs', 'sm', 'md'].includes(v as string | number))).toBe(true)
  }
  // The to do list, warnings and sections md apart; a row's lines xs
  expect(all.find(el => el.type === 'Box' && el.props.gap !== undefined)?.props.gap).toBe('md')
  // Only the bar and the row hairlines fill: green on a 22% grey
  expect([...used('Box', 'backgroundColor')]).toEqual(['rgba(128, 128, 128, 0.22)', 'success'])
  // The icons' hues are the tones' (a merged PR muted), each about 4.2:1 on light and dark
  const hexes = new Set(all.filter(el => el.type === 'Svg').flatMap(el => String(el.props.source).match(/#[0-9A-Fa-f]{3,6}\b/g) ?? []))
  expect([...hexes].every(hex => ['#278D43', '#7B7B7B', '#A27300', '#E33C41', '#fff'].includes(hex))).toBe(true)
  expect(all.find(el => el.type === 'Svg' && el.props.alt === 'PR #7 merged')?.props.source).toContain('#7B7B7B')
  // A section's header takes its dot's color: Working green, not the accent
  const header = (group: string) => all.find(el => el.type === 'Text' && el.text === group)?.props.color
  expect(['NEEDS YOU', 'WORKING', 'WAITING', 'IDLE'].map(header)).toEqual(['error', 'success', 'warning', 'subtle'])
  await ui.unmount()
})

// The pane's width in cells, read as about 8 px a cell on the desktop: 390, 768 and 1440 px
const WIDTHS = { 390: 48, 768: 96, 1440: 180 } as const

test('the desktop reads no width: the same tree at 390, 768 and 1440 px, and what never shrinks on a row fits 390', async ($, on) => {
  await startFull($, on)
  const trees: string[] = []
  for (const bodyColumns of Object.values(WIDTHS)) {
    const ui = await $.ui.mount({ ...PANE, props: { ...PANE.props, bodyColumns }, surface: 'desktop' })
    // What is open is the session's, so the later mounts find it open
    if (trees.length === 0) await openAll(ui)
    const all = await ui.findAll({})
    trees.push(JSON.stringify(all.map(el => [el.type, el.key, el.text])))
    // A row Box that neither wraps nor shrinks below its content holds text that fits the narrow width
    const rigid = all.filter(el => el.type === 'Box' && el.props.flexDirection === 'row' && el.props.flexWrap !== 'wrap' && el.props.minWidth !== 0)
    expect(rigid.map(el => shown(el as Drawn)).filter(text => text.length > WIDTHS[390] - 8)).toEqual([])
    // Line 1 shrinks its name and what it says; its fixed parts (dot, buttons, time) fit
    for (const id of ['local_n', 'local_w', 'local_p', 'local_q']) {
      const cells = ((await ui.find({ key: `${id}:header` }))?.children ?? []) as (Drawn & { props: Record<string, unknown> })[]
      const fixed = cells.filter(c => c.props.flexShrink === 0).map(shown).join(' ')
      expect(fixed.length).toBeLessThan(WIDTHS[390] / 2)
    }
    await ui.unmount()
  }
  expect(new Set(trees).size).toBe(1)
})

test('the terminal and the phones keep their own looks: theme greys, a caption, medium, dim text, the GitHub hues', async ($, on) => {
  await startFull($, on)
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await openAll(ui)
  const all = await ui.findAll({})
  const used = (type: string, prop: string) => new Set(all.filter(el => el.type === type).map(el => el.props[prop]))
  expect([...used('Text', 'color')]).toEqual(expect.arrayContaining(['suggestion', 'inactive']))
  expect(used('Text', 'size').has('caption')).toBe(true)
  expect(used('Text', 'weight').has('medium')).toBe(true)
  expect(used('Text', 'dimColor').has(true)).toBe(true)
  expect(used('Button', 'dimColor').has(true)).toBe(true)
  expect(all.find(el => el.type === 'Box' && el.props.gap !== undefined)?.props.gap).toBe('sm')
  const header = (group: string) => all.find(el => el.type === 'Text' && el.text === group)?.props.color
  expect(['NEEDS YOU', 'WORKING', 'WAITING', 'IDLE'].map(header)).toEqual(['error', 'suggestion', 'warning', 'inactive'])
  expect((await ui.find({ key: 'local_s:row' }))?.children[0]).toMatchObject({ props: { color: 'inactive' } })
  // Back as the one quiet worker: its line, no toggle, its name as a row's
  await ui.press({ key: 'local_s:fold' })
  expect(await ui.find({ key: 'idle:fold' })).toBeFalsy()
  const quiet = (await ui.find({ key: 'local_s:fold' }))?.children[0] as { props: Record<string, unknown> } | undefined
  expect(quiet?.props.weight).toBe('semibold')
  expect(Object.hasOwn(quiet?.props ?? {}, 'color')).toBe(false)
  await ui.unmount()
  const mobile = await $.ui.mount({ ...PANE, surface: 'mobile' })
  const svgs = (await mobile.findAll({ type: 'Svg' })).map(el => String(el.props.source)).join('')
  for (const hex of ['#2DA44E', '#8957E5', '#E5484D']) expect(svgs).toContain(hex)
  expect(svgs).not.toContain('#278D43')
  await mobile.unmount()
})

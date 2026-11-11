import type { On, UiPane } from 'claude-code'
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

/** What the other commands the mod runs answer: gh pr checks, dev delivered, dev desktop answer, open, mv (by argv[0]/argv[1]/argv[2]). */
type Others = Partial<Record<'gh' | 'delivered' | 'answer' | 'open' | 'mv', Ran | string | (() => Ran | string)>>

/** Every run of a command other than `dev desktop board`, with its stdin. */
type OtherRun = { argv: readonly string[]; stdin?: string }

/**
 * Starts the session with `dev desktop board` answering `answer()`'s run, or failing to start with that reason;
 * `others` answers the rest (gh's checks none, dev delivered exits 1, open passes by default); `env` adds to its environment;
 * `panes` lists the mod's open panes (none by default).
 */
async function start($: Engine, on: On, ran: Ran | string | (() => Ran | string), others: Others = {}, env: Record<string, string> = {}, panes: () => UiPane[] = () => []) {
  const answer = typeof ran === 'function' ? ran : () => ran
  const runs: (readonly string[])[] = []
  const otherRuns: OtherRun[] = []
  on('command.register', () => ({ value: { command: 'board' } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('ui.panes', () => ({ value: panes() }))
  on('process.run', (_, e) => {
    const isBoard = e.argv[1] === 'desktop' && e.argv[2] === 'board'
    const other = others[e.argv[1] === 'delivered' ? 'delivered' : e.argv[2] === 'answer' ? 'answer' : (e.argv[0] as 'gh' | 'open' | 'mv')]
    const now = isBoard
      ? answer()
      : ((typeof other === 'function' ? other() : other) ??
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
  mock.env(on, { CLAUDE_CODE_HOST_SESSION_ID: 'local_boss', ...env })
  const clock = mock.clock(on)
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await clock.settle()
  return { runs, otherRuns, clock }
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

/** A tile's header row as read: its Texts, split by a space; the fold button left out. */
async function header(ui: Mounted, id = 'local_a') {
  return (await textsOf(ui, `${id}:header`))?.join(' ')
}

/** Unfolds a worker's tile if folded (an idle worker's is, by default). */
async function unfold(ui: Mounted, id = 'local_a') {
  if ((await ui.find({ key: `${id}:fold` }))?.text === '▸') {
    await ui.press({ key: `${id}:fold` })
  }
}

test('the pane shows a tile per worker, on every surface', async ($, on) => {
  const { runs, clock } = await start($, on, ok(WORKERS))
  await clock.advance(2000)
  expect(runs[0]).toEqual(['dev', 'desktop', 'board', '--json', '--boss', 'local_boss'])
  expect(runs.length).toBe(2)

  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect(await header(ui)).toBe('Worker A')
    // Its status left out: the Working section says it
    expect((await ui.find({ type: 'Text', text: /^Bash$/ }))?.text).toBeTruthy()
    expect(await ui.find({ type: 'Text', text: /^working/ })).toBeFalsy()
    expect((await ui.find({ type: 'Text', text: /^feature\/a$/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /^· 42%$/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /Running the tests/ }))?.text).toBe('Running the tests.')
    expect((await ui.find({ type: 'Text', text: /last turn: completed/ }))?.text).toBe('last turn: completed')
    expect(await header(ui, 'local_b')).toBe('Worker B')
    // The status, its need and the verdict's detail, each said once
    expect((await ui.findAll({ type: 'Text', text: /blocked|pick one/ })).map(t => t.text)).toEqual(['blocked · pick one'])
    expect((await ui.find({ type: 'Text', text: /^two ways$/ }))?.text).toBeTruthy()
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

test('warnings show above the tiles, a row each: ⚠, then its line without dev\'s prefix', async ($, on) => {
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
  expect((await textsOf(ui, 'error'))?.[1]).toMatch(/is dev up to date/)
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

test('the task list: Tasks, a bar and its count above the steps; collapsed, the step at hand on the same row', async ($, on) => {
  await start($, on, ok([worker({ plan: [...PLAN] })]))
  const folded = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await header(folded)).toBe('Worker A 1/3')
  await folded.unmount()
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    await unfold(ui)
    // Open, the count moves from the header to the list
    expect(await header(ui)).toBe('Worker A')
    // The label in the steps' text size, lined up with their marks; the toggle, its count and arrow; collapsed, the step at hand in a cell that shrinks
    const row = (await ui.find({ key: 'local_a:plan' }))?.children[0] as Cell | undefined
    expect(row?.children.map(c => (c as { type?: string }).type)).toEqual(['Text', 'Box', 'Button', 'Box'])
    expect(row?.props).toMatchObject({ flexDirection: 'row', minWidth: 0 })
    expect(row?.children[3]?.props).toMatchObject({ flexShrink: 1, minWidth: 0 })
    expect((await ui.find({ key: 'local_a:plan' }))?.children.length).toBe(1)
    expect(row?.children[0]?.props).toMatchObject({ size: 'footnote', color: 'subtle' })
    expect(row?.children[0]?.children).toEqual(['Tasks'])
    expect((await ui.find({ key: 'local_a:tasks' }))?.props).toMatchObject({ plain: true })
    expect((await ui.find({ type: 'Text', text: /^1\/3 ▸$/ }))?.props).toMatchObject({ size: 'footnote', tabular: true })
    // The bar: on the desktop a track, its done part grown by the steps done against those left; on the terminal whole cells
    const bar = await ui.find({ key: 'local_a:bar' })
    if (surface === 'terminal') {
      // The empty part thin and dim, so an empty bar reads as empty
      expect(((bar?.children[0] as Cell | undefined)?.children ?? []).map(c => [c.props.color, c.props.dimColor, c.children.join('')])).toEqual([['success', undefined, '━━━'], ['inactive', true, '───────']])
    } else {
      const track = bar?.children[0] as Cell | undefined
      // A faint track, not a theme grey that draws as a full bar
      expect(track?.props.backgroundColor).toMatch(/^rgba\(/)
      expect(track?.children.map(c => [c.props.flexGrow, c.props.backgroundColor])).toEqual([[1, 'success'], [2, undefined]])
    }
    // Collapsed, only the step at hand
    expect((await stepAt(ui, 'local_a:step1')).text).toBe('Write the mod')
    expect(await ui.find({ type: 'Text', text: 'Read the code' })).toBeFalsy()
    await ui.press({ key: 'local_a:tasks' })
    expect((await ui.find({ type: 'Text', text: /^1\/3 ▾$/ }))?.text).toBeTruthy()
    // Expanded, the toggle ends the row, the steps under it
    expect(((await ui.find({ key: 'local_a:plan' }))?.children[0] as Cell | undefined)?.children.map(c => (c as { type?: string }).type)).toEqual(['Text', 'Box', 'Button'])
    // A row per step: its mark and its text in cells of their own, the text cut to one line
    const steps = await Promise.all(PLAN.map((_, i) => stepAt(ui, `local_a:step${i}`)))
    expect(steps.map(s => s.shrink)).toEqual([[0, 1], [0, 1], [0, 1]])
    expect(steps.map(s => [s.mark, s.text])).toEqual([['●', 'Read the code'], ['◐', 'Write the mod'], ['○', 'Open the PR']])
    expect(steps.every(s => s.textProps?.wrap === 'truncate-end')).toBe(true)
    // Done recedes, the current step stands out in the section's color, pending stays quiet
    expect(steps.map(s => [s.markProps?.color, s.markProps?.dimColor])).toEqual([['success', true], ['inactive', false], ['subtle', false]])
    expect(steps.map(s => [s.textProps?.color, s.textProps?.weight, s.textProps?.dimColor])).toEqual([['inactive', 'regular', true], ['text', 'medium', false], ['subtle', 'regular', false]])
    // Above the buttons
    const order = (await ui.findAll({})).map(el => el.key ?? el.text)
    expect(order.indexOf('local_a:plan')).toBeGreaterThan(-1)
    expect(order.indexOf('local_a:plan')).toBeLessThan(order.indexOf('local_a:open'))
    await ui.press({ key: 'local_a:tasks' })
    expect(await ui.find({ type: 'Text', text: 'Read the code' })).toBeFalsy()
    await ui.unmount()
  }
})

test('a long task list folds the done steps but the last into a count, and those past the room into more', async ($, on) => {
  const plan = Array.from({ length: 12 }, (_, i) => ({ content: `Step ${i}`, status: i < 6 ? 'completed' : i === 6 ? 'in_progress' : 'pending' }) as const)
  await start($, on, ok([worker({ state: 'busy', status: 'working', plan })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:tasks' })
  const rows = ((await ui.find({ key: 'local_a:plan' }))?.children ?? []).slice(1) as { props: { key?: string } }[]
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
    await ui.press({ key: `local_${i}:tasks` })
    const rows = ((await ui.find({ key: `local_${i}:plan` }))?.children ?? []).slice(1) as { props: { key?: string } }[]
    const shown = await Promise.all(rows.map(r => stepAt(ui, r.props.key ?? '')))
    expect(shown.map(s => `${s.mark} ${s.text}`)).toEqual(c.rows)
  }
  await ui.unmount()
})

test('a collapsed task list with no step in progress shows the next pending one', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', plan: [PLAN[0], { content: 'Write the mod', status: 'pending' }, PLAN[2]] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ key: 'local_a:plan' }))?.children.length).toBe(1)
  expect((await stepAt(ui, 'local_a:step1')).text).toBe('Write the mod')
  await ui.unmount()
})

test('a current step past the room of a long task list still shows, in the last row', async ($, on) => {
  const plan = Array.from({ length: 10 }, (_, i) => ({ content: `Step ${i}`, status: i < 3 ? 'completed' : i === 9 ? 'in_progress' : 'pending' }) as const)
  await start($, on, ok([worker({ state: 'busy', status: 'working', plan })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:tasks' })
  const rows = ((await ui.find({ key: 'local_a:plan' }))?.children ?? []).slice(1) as { props: { key?: string } }[]
  const shown = await Promise.all(rows.map(r => stepAt(ui, r.props.key ?? '')))
  expect(shown.map(s => `${s.mark} ${s.text}`)).toEqual(['● 2 done', '● Step 2', '○ Step 3', '○ Step 4', '○ Step 5', '◐ Step 9', '○ 3 more'])
  await ui.unmount()
})

test('a collapsed task list with every step done shows no step under its bar', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', plan: PLAN.map(s => ({ ...s, status: 'completed' as const })) })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^3\/3 ▸$/ }))?.text).toBeTruthy()
  expect(await ui.find({ type: 'Text', text: /Read the code|Write the mod|Open the PR/ })).toBeFalsy()
  await ui.unmount()
})

test('no task list: no count and no Tasks button', async ($, on) => {
  await start($, on, ok([worker({})]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await header(ui)).toBe('Worker A')
  await unfold(ui)
  expect(await ui.find({ key: 'local_a:tasks' })).toBeFalsy()
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
  expect([await header(ui, 'local_b'), await header(ui)]).toEqual(['Worker B 5m', 'Worker A 12m'])
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
  expect(await header(ui)).toBe('Worker A 4m')
  expect((await ui.findAll({ type: 'Text', text: /^(waiting|Auto-fix)/ })).map(t => t.text)).toEqual(['Auto-fix: PR #80'])
  await ui.unmount()
})

const OPEN_PR = { number: 80, url: 'https://github.com/o/r/pull/80', state: 'OPEN' }
const CHECKS: Ran = {
  exitCode: 8,
  stdout: JSON.stringify([{ name: 'test', bucket: 'pass' }, { name: 'lint', bucket: 'fail' }, { name: 'e2e', bucket: 'pending' }]),
  stderr: '',
}

/** A PR's row as read: its Texts and its icons, an Svg by its alt (desktop), a glyph as its text (terminal). */
async function prRow(ui: Mounted, key = `local_a:${OPEN_PR.url}`) {
  type El = string | { type: string; props?: Record<string, unknown>; children?: El[] }
  const text = (el: El): string => (typeof el === 'string' ? el : el.type === 'Link' ? String(el.props?.label) : (el.children ?? []).map(text).join(''))
  const read = (el: El): string[] => {
    if (typeof el === 'string') return []
    if (el.type === 'Svg') return [`[${String(el.props?.alt)}]`]
    if (el.type === 'Text' || el.type === 'Link') return [text(el)]
    return (el.children ?? []).flatMap(read)
  }
  const row = await ui.find({ key })
  return row === undefined ? undefined : (row.children as El[]).flatMap(read)
}

test("an open PR's row: its icon, link and state, then its CI icon and the checks that make it; read once a minute", async ($, on) => {
  const { otherRuns, clock } = await start($, on, ok([worker({ state: 'busy', status: 'working', prs: [OPEN_PR] })]), { gh: CHECKS })
  const gh = () => otherRuns.filter(r => r.argv[0] === 'gh')
  expect(gh().map(r => r.argv)).toEqual([['gh', 'pr', 'checks', OPEN_PR.url, '--json', 'name,bucket']])
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^feature\/a$/ }))?.text).toBeTruthy()
  // A failed check outweighs a running one
  expect(await prRow(ui)).toEqual(['[PR #80 open]', 'PR #80 open', '[CI failed]', 'lint'])
  // One line: the PR never wraps, its check names cut
  expect((await ui.find({ key: `local_a:${OPEN_PR.url}` }))?.props).toMatchObject({ flexDirection: 'row', minWidth: 0 })
  expect((await ui.find({ key: `local_a:${OPEN_PR.url}` }))?.props.flexWrap).toBeUndefined()
  expect((await ui.find({ type: 'Text', text: 'lint' }))?.props.wrap).toBe('truncate-end')
  await ui.unmount()
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await prRow(terminal)).toEqual(['○', 'PR #80 open', '✗', 'lint'])
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
      expect(await prRow(ui)).toEqual([state, 'PR #80 open', ...ci])
      await ui.unmount()
    }
  })
}

const CLOSED_PR = { ...OPEN_PR, number: 81, url: 'https://github.com/o/r/pull/81', state: 'CLOSED' }

test('a merged or closed PR shows its own icon and no CI icon', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', prs: [{ ...OPEN_PR, state: 'MERGED' }, CLOSED_PR] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await prRow(ui)).toEqual(['[PR #80 merged]', 'PR #80 merged'])
  expect(await prRow(ui, `local_a:${CLOSED_PR.url}`)).toEqual(['[PR #81 closed]', 'PR #81 closed'])
  // Each its own shape, not one recolored
  const sources = (await ui.findAll({ type: 'Svg' })).map(s => String(s.props.source).replace(/#[0-9A-F]{6}/gi, ''))
  expect(new Set(sources).size).toBe(2)
  await ui.unmount()
  const terminal = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await prRow(terminal)).toEqual(['◆', 'PR #80 merged'])
  expect(await prRow(terminal, `local_a:${CLOSED_PR.url}`)).toEqual(['✕', 'PR #81 closed'])
  expect((await terminal.findAll({ type: 'Text', text: /^[◆✕]$/ })).map(t => t.props.color)).toEqual(['merged', 'error'])
  await terminal.unmount()
})

test('checks read while a PR was open show no more once it merged', async ($, on) => {
  let prs = [OPEN_PR]
  const { clock } = await start($, on, () => ok([worker({ state: 'busy', status: 'working', prs })]), { gh: CHECKS })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await prRow(ui)).toEqual(['[PR #80 open]', 'PR #80 open', '[CI failed]', 'lint'])
  prs = [{ ...OPEN_PR, state: 'MERGED' }]
  await clock.advance(2000)
  expect(await prRow(ui)).toEqual(['[PR #80 merged]', 'PR #80 merged'])
  await ui.unmount()
})

test("a folded tile shows its PRs' icons in its header, in order", async ($, on) => {
  await start($, on, ok([worker({ state: 'idle', status: 'idle', prs: [OPEN_PR, CLOSED_PR] })]), { gh: checksOf('pass') })
  for (const [surface, icons] of [['desktop', ['[PR #80 open]', '[CI passed]', '[PR #81 closed]']], ['terminal', ['○', '✓', '✕']]] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    const header = await prRow(ui, 'local_a:header')
    expect(header?.filter(t => icons.includes(t as never))).toEqual([...icons])
    expect(await ui.find({ key: `local_a:${OPEN_PR.url}` })).toBeFalsy()
    await ui.unmount()
  }
})

test('checks that could not be read say why', async ($, on) => {
  await start($, on, ok([worker({ prs: [OPEN_PR] })]), { gh: 'spawn gh ENOENT' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui)
  const row = await prRow(ui)
  expect(row?.slice(0, 2)).toEqual(['[PR #80 open]', 'PR #80 open'])
  expect(row?.[2]).toMatch(/^gh pr checks: .*ENOENT/)
  expect((await ui.find({ type: 'Text', text: /ENOENT/ }))?.props).toMatchObject({ dimColor: true, wrap: 'truncate-end' })
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
  expect((await ui.find({ type: 'Text', text: /^needs:/ }))?.text).toBe('needs: pick one')
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

test('a question with options says it once: no needs line, no reply line restating it or listing its options; a verdict with news kept', async ($, on) => {
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
  expect(await ui.find({ type: 'Text', text: /^needs:/ })).toBeFalsy()
  expect(await ui.find({ type: 'Text', text: /^Pick a reply/ })).toBeFalsy()
  expect((await ui.findAll({ type: 'Text', text: /^Report format\?$/ })).length).toBe(2)
  expect((await ui.find({ type: 'Text', text: /^blocked: counted 144/ }))?.text).toBe('blocked: counted 144 .go files under internal/; awaiting report format')
  // The line before it takes the room beside the verdict
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
  expect((await ui.findAll({ type: 'Text', text: /^Choose how/ })).length).toBe(1)
  expect((await ui.findAll({ type: 'Text', text: /^needs:/ })).map(t => t.text)).toEqual(['needs: choose how the report groups files'])
  await ui.unmount()
})

test('a need beside a question without options still shows', async ($, on) => {
  const open = { question: 'What should the report say?', header: '', multiSelect: false, options: [] }
  await start($, on, ok([worker({ status: 'idle', needsAction: 'describe the report', ask: { toolUseId: 'w1', questions: [open], askedAt: 0, answerable: false, reply: true } })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^needs:/ }))?.text).toBe('needs: describe the report')
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

test('idle and stopped tiles fold to one line; the rest stay open', async ($, on) => {
  await start($, on, ok(BY_STATUS))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  for (const id of ['local_i', 'local_s']) {
    expect((await ui.find({ key: `${id}:fold` }))?.text).toBe('▸')
    expect(await ui.find({ key: `${id}:open` })).toBeFalsy()
  }
  for (const id of ['local_w', 'local_r', 'local_b', 'local_k', 'local_t', 'local_l', 'local_n']) {
    expect((await ui.find({ key: `${id}:fold` }))?.text).toBe('▾')
    expect(await ui.find({ key: `${id}:open` })).toBeTruthy()
  }
  expect(await header(ui, 'local_i')).toBe('Idle')
  // Dimmed once, by a theme color; an open title not at all
  const name = await ui.find({ type: 'Text', text: /^Idle$/ })
  expect([name?.props.color, name?.props.dimColor]).toEqual(['subtle', undefined])
  expect((await ui.find({ type: 'Text', text: /^Waiting$/ }))?.props.color).toBeUndefined()
  await ui.unmount()
})

test('an idle tile with a health note opens; folded by the person, it ends in ⚠', async ($, on) => {
  const { clock } = await start($, on, ok([worker({ state: 'stopped', status: 'stopped', notes: [RESUME] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^resume due:/ }))?.text).toBe('resume due: rate limit reset 18:05: resume it')
  await ui.press({ key: 'local_a:fold' })
  await clock.advance(2000)
  expect(await ui.find({ type: 'Text', text: /^resume due:/ })).toBeFalsy()
  expect(await header(ui)).toBe('Worker A · stopped ⚠')
  expect((await ui.find({ type: 'Text', text: /^⚠$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('a status this mod does not know shows under Working, open', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'paused' as Worker['status'] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.findAll({ type: 'Text', text: /^(WORKING$|paused)/ })).map(t => t.text)).toEqual(['WORKING', 'paused'])
  expect(await header(ui)).toBe('Worker A')
  expect(await ui.find({ key: 'local_a:open' })).toBeTruthy()
  await ui.unmount()
})

test("only the newest reply's last two lines show", async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', lastLines: ['one', 'two', 'three'] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ type: 'Text', text: /^one$/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^two$/ }))?.text).toBeTruthy()
  expect((await ui.find({ type: 'Text', text: /^three$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('beside a verdict only the last reply line shows', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', verdict: 'completed', lastLines: ['one', 'two', 'three'] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await textsOf(ui, 'local_a:info'))?.slice(-2)).toEqual(['last turn: completed', 'three'])
  await ui.unmount()
})

test('a verdict reads as words, not its snake_case', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', verdict: 'review_ready', detail: 'board ask fix' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text', text: /^last turn: / }))?.text).toBe('last turn: review ready: board ask fix')
  await ui.unmount()
})

test("a fold or unfold sticks across polls and status changes, per worker", async ($, on) => {
  let workers = [worker({ state: 'busy', status: 'working' }), worker({ id: 'local_b', title: 'Worker B' }), worker({ id: 'local_c', title: 'Worker C' })]
  const { clock } = await start($, on, () => ok(workers))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const label = async (id: string) => (await ui.find({ key: `${id}:fold` }))?.text
  await ui.press({ key: 'local_a:fold' })
  await ui.press({ key: 'local_b:fold' })
  expect([await label('local_a'), await label('local_b'), await label('local_c')]).toEqual(['▸', '▾', '▸'])

  // Defaults now the other way: open for A, folded for B; the choices stand
  workers = [worker({ status: 'waiting', since: '3m' }), worker({ id: 'local_b', title: 'Worker B', state: 'stopped', status: 'stopped', since: '1m' }), worker({ id: 'local_c', title: 'Worker C', since: '9m' })]
  await clock.advance(4000)
  expect([await label('local_a'), await label('local_b'), await label('local_c')]).toEqual(['▸', '▾', '▸'])
  expect(await ui.find({ key: 'local_b:open' })).toBeTruthy()
  expect(await ui.find({ key: 'local_a:open' })).toBeFalsy()
  await ui.unmount()

  const again = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await again.find({ key: 'local_a:fold' }))?.text).toBe('▸')
  expect((await again.find({ key: 'local_b:fold' }))?.text).toBe('▾')
  await again.unmount()
})

test('sections group the workers by status, with counts, needs you first; empty ones hidden', async ($, on) => {
  let workers = BY_STATUS
  const { clock } = await start($, on, () => ok(workers))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  // Section headers, their counts and the tiles' headers, in order: a tile by its name
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
    'Idle',
    'Stopped',
  ])
  // The count beside its header, not in the header's color
  expect((await ui.find({ type: 'Text', text: /^2$/ }))?.props.color).toBe('subtle')

  workers = BY_STATUS.filter(w => w.status === 'idle' || w.status === 'working')
  await clock.advance(2000)
  expect(await rows()).toEqual(['WORKING', '1', 'Working', 'IDLE', '1', 'Idle'])
  await ui.unmount()
})

test('a message being typed keeps its tile open as the worker goes idle; folding it drops the field', async ($, on) => {
  let workers = [worker({ state: 'busy', status: 'working' })]
  const { clock } = await start($, on, () => ok(workers))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:message' })
  workers = [worker({})]
  await clock.advance(2000)
  expect(await ui.find({ key: 'local_a:draft' })).toBeTruthy()
  await ui.press({ key: 'local_a:fold' })
  expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
  await ui.press({ key: 'local_a:fold' })
  expect(await ui.find({ key: 'local_a:draft' })).toBeFalsy()
  await ui.unmount()
})

test('a verdict the status says, with no detail, shows no line', async ($, on) => {
  await start($, on, ok([worker({ state: 'blocked', status: 'blocked', verdict: 'blocked' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.findAll({ type: 'Text', text: /blocked/ })).map(t => t.text)).toEqual(['blocked'])
  await ui.unmount()
})

test('Open is the main button for Needs you and Waiting tiles without a question to press; the row is Open, Message…, then Archive', async ($, on) => {
  await start($, on, ok([...BY_STATUS, worker({ id: 'local_p', title: 'Delivered', prs: [OPEN_PR] })]), { delivered: DELIVERED })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui, 'local_i')
  await unfold(ui, 'local_p')
  const variant = async (id: string) => (await ui.find({ key: `${id}:open` }))?.props.variant
  expect(await Promise.all(['local_n', 'local_w', 'local_b', 'local_k', 'local_i'].map(variant))).toEqual(['primary', 'primary', 'primary', undefined, undefined])
  // Working and Idle tiles' Open and Message… are dim text; Archive keeps its pill
  const looks = async (key: string) => {
    const { plain, dimColor } = (await ui.find({ key }))?.props ?? {}
    return [plain ?? false, dimColor ?? false]
  }
  const keys = ['local_n:open', 'local_w:message', 'local_k:open', 'local_k:message', 'local_i:open', 'local_i:message', 'local_p:archive']
  expect(await Promise.all(keys.map(looks))).toEqual([[false, false], [false, false], [true, true], [true, true], [true, true], [true, true], [false, false]])
  // Archive shows for a delivered worker not busy with a PR
  const row = async (id: string) => (await ui.findAll({ type: 'Button' })).map(b => b.key).filter(k => k?.startsWith(`${id}:`) && k !== `${id}:fold`)
  expect(await row('local_i')).toEqual(['local_i:open', 'local_i:message'])
  expect(await row('local_p')).toEqual(['local_p:open', 'local_p:message', 'local_p:archive'])
  await ui.unmount()
})

test("on a tile asking a question, Open and Message… are dim text: the question's options are its buttons", async ($, on) => {
  await start($, on, ok([asking({ toolUseId: 'tu1', questions: [DB], askedAt: 1, answerable: true })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const looks = async (key: string) => {
    const { plain, dimColor, variant } = (await ui.find({ key }))?.props ?? {}
    return [plain, dimColor, variant]
  }
  expect(await looks('local_a:open')).toEqual([true, true, undefined])
  expect(await looks('local_a:message')).toEqual([true, true, undefined])
  await ui.unmount()
})

test("a reply's last lines drop their inline markdown, code kept as is", async ($, on) => {
  const lines = ['- Grant **full-screen** control (`request_full_control`)', '* keep `__init__` as is, __bold__', '**Done**: src/**/*.ts', '```']
  await start($, on, ok([worker({ state: 'busy', status: 'working', lastLines: lines })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  // The closing fence is skipped, so the two lines before it show
  expect(await ui.find({ type: 'Text', text: /^Grant/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^keep/ }))?.text).toBe('keep __init__ as is, bold')
  expect((await ui.find({ type: 'Text', text: /^Done/ }))?.text).toBe('Done: src/**/*.ts')
  await ui.unmount()
})

test('a list line drops its marker and bold, code kept as is', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', lastLines: ['- Grant **full-screen** control (`request_full_control`)'] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
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

test('tiles shrink to the pane: the boxes the cut rows sit in may shrink below their content', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', statusText: 'Bash' }), worker({ id: 'local_b', title: 'Worker B' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  for (const id of ['local_a', 'local_b']) {
    expect((await ui.find({ key: id }))?.props).toMatchObject({ flexDirection: 'column', minWidth: 0 })
  }
  const boxes = await ui.findAll({ type: 'Box' })
  // Each tile's card; the open one's rows, header row, title cell, meta row and its branch cell; the folded row and its title cell
  const shrinking = boxes.filter(b => b.props.borderStyle !== undefined || b.props.flexShrink === 1 || (b.props.flexDirection === 'row' && b.props.minWidth !== undefined))
  expect(shrinking.length).toBe(9)
  expect(shrinking.every(b => b.props.minWidth === 0)).toBe(true)
  // The titles are cut; the open one's progress and time never shrink
  for (const title of ['Worker A', 'Worker B']) {
    expect((await ui.find({ type: 'Text', text: title }))?.props.wrap).toBe('truncate-end')
  }
  expect(boxes.filter(b => b.props.flexShrink === 0).length).toBe(1)
  await ui.unmount()
})

test("a review-ready tile says its detail once, on the status row", async ($, on) => {
  await start($, on, ok([worker({ status: 'review ready', statusText: 'PR #80 green', verdict: 'review_ready', detail: 'PR #80 green' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.findAll({ type: 'Text', text: /review|green/ })).map(t => t.text)).toEqual(['review ready · PR #80 green'])
  await ui.unmount()
})

test("a folded tile's status and time follow its title, split by ·", async ($, on) => {
  await start($, on, ok([worker({ title: 'Stopped', state: 'stopped', status: 'stopped', since: '5m' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await header(ui)).toBe('Stopped · stopped · 5m')
  // Its time never shrinks: a desktop Text not cut wraps anywhere once squeezed
  expect((await ui.findAll({ type: 'Box' })).filter(b => b.props.flexShrink === 0).map(b => b.text)).toEqual(['· 5m'])
  await ui.unmount()
})

test('the status row is one cut line, without inline markdown', async ($, on) => {
  const needs = 'run `dev delivered` on **feature/a**, then merge PR #80 once its checks pass and the review is in'
  await start($, on, ok([
    worker({ state: 'blocked', status: 'blocked', statusText: needs, needsAction: needs, verdict: 'blocked' }),
    worker({ id: 'local_b', title: 'Worker B', state: 'stopped', status: 'stopped', statusText: 'a dialog was open when the app quit' }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const status = await ui.find({ type: 'Text', text: /^blocked · / })
  expect(status?.text).toBe('blocked · run dev delivered on feature/a, then merge PR #80 once its checks pass and the review is in')
  expect(status?.props.wrap).toBe('truncate-end')
  // Folded, cut in its own shrinking cell, the size of the row it sits in
  const folded = (await ui.find({ type: 'Text', text: /^· stopped · a dialog/ }))?.props
  expect([folded?.wrap, folded?.size, folded?.color]).toEqual(['truncate-end', 'footnote', 'subtle'])
  await ui.unmount()
})

test('a cut Text holds only strings: a Text nested in it wraps on the desktop', async ($, on) => {
  await start($, on, ok([...BY_STATUS, worker({ id: 'local_p', plan: [...PLAN], since: '5m', statusText: 'x', lastLines: ['**done**'], verdict: 'completed', detail: 'ok' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const cut = (await ui.findAll({ type: 'Text' })).filter(t => t.props.wrap === 'truncate-end')
  expect(cut.length).toBeGreaterThan(BY_STATUS.length)
  expect(cut.filter(t => t.children.some(c => typeof c !== 'string')).map(t => t.text)).toEqual([])
  await ui.unmount()
})

test('the fold arrow sits on the title line, open or folded', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working' }), worker({ id: 'local_b', title: 'Worker B' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const foldRow = async (id: string) =>
    (await ui.findAll({ type: 'Box' })).find(b => b.children.some(c => (c as { props?: { key?: string } }).props?.key === `${id}:fold`))
  for (const id of ['local_a', 'local_b']) {
    expect((await foldRow(id))?.props).toMatchObject({ flexDirection: 'row', alignItems: 'flex-start' })
  }
  expect((await ui.find({ key: 'local_b:fold' }))?.text).toBe('▸')
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

test("an open tile's rows all start under its title, the fold button and dot beside them", async ($, on) => {
  await start($, on, ok([worker({ state: 'blocked', status: 'blocked', statusText: 'pick one', context: 10, lastLines: ['Done.'], plan: [...PLAN] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:tasks' })
  const rows = await ui.find({ key: 'local_a:rows' })
  expect(rows?.props.flexDirection).toBe('column')
  const shown = JSON.stringify(rows?.children)
  for (const row of ['pick one', 'local_a:meta', 'feature/a', '· 10%', 'Done.', 'Write the mod', 'local_a:open']) {
    expect(shown).toContain(row)
  }
  expect(shown).not.toContain('local_a:fold')
  expect(shown).not.toContain('"paddingLeft"')
  const tile = (await ui.findAll({ type: 'Box' })).find(b => b.props.borderStyle === 'quote')
  const beside = (tile?.children as { type: string; props: { key?: string } }[]).map(c => c.props.key ?? c.type)
  expect(beside).toEqual(['local_a:fold', 'Text', 'local_a:rows'])
  await ui.unmount()
})

test('Message… opens one row: a short field, Send and Cancel', async ($, on) => {
  const texts = submits(on)
  const toasts = collectToasts(on)
  await start($, on, ok([worker({ state: 'busy', status: 'working', name: 'demo-waiting' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:message' })
  const field = await ui.find({ key: 'local_a:draft' })
  // Named by its label, not its key
  expect(field?.props).toMatchObject({ label: 'Message demo-waiting', autoFocus: true })
  expect(field?.props.submitLabel).toBeUndefined()
  expect((await ui.find({ key: 'local_a:send' }))?.props).toMatchObject({ label: 'Send', variant: 'primary' })
  expect((await ui.find({ key: 'local_a:cancel' }))?.props).toMatchObject({ label: 'Cancel', plain: true })
  const row = (await ui.findAll({ type: 'Box' })).find(b => b.children.some(c => (c as { props?: { key?: string } }).props?.key === 'local_a:draft'))
  expect(row?.props.flexDirection).toBe('row')

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

  // Closed by Message… again, or by a fold, its text goes too
  for (const close of [['local_a:message'], ['local_a:fold', 'local_a:fold']]) {
    await ui.press({ key: 'local_a:message' })
    await ui.input({ key: 'local_a:draft', text: 'stale', kind: 'change' })
    for (const key of close) await ui.press({ key })
    await ui.press({ key: 'local_a:message' })
    await ui.press({ key: 'local_a:send' })
    await ui.press({ key: 'local_a:cancel' })
  }
  expect(texts).toEqual(['dev-board: message demo-waiting (local_a): add tests'])
  await ui.unmount()
})

const DB = { question: 'Which DB?', header: 'DB', multiSelect: false, options: [{ label: 'Postgres', description: 'relational' }, { label: 'Mongo', description: '' }] }
const FEATURES = { question: 'Which features?', header: 'Features', multiSelect: true, options: [{ label: 'Auth', description: 'sign-in' }, { label: 'Search', description: 'full text' }] }
const asking = (ask: Worker['ask']) => worker({ state: 'busy', status: 'needs you', statusText: 'Which DB? (Postgres / Mongo)', ask })

test('a question shows on its tile: header and question, its options buttons in one wrapping row, the first leading, their descriptions a caption under it', async ($, on) => {
  await start($, on, ok([asking({ toolUseId: 'tu1', questions: [DB], askedAt: 1, answerable: true })]))
  for (const surface of ['terminal', 'desktop'] as const) {
    const ui = await $.ui.mount({ ...PANE, surface })
    expect((await ui.find({ type: 'Text', text: /^DB$/ }))?.text).toBeTruthy()
    expect((await ui.find({ type: 'Text', text: /^Which DB\?$/ }))?.text).toBeTruthy()
    const options = await ui.find({ key: 'local_a:q0:options' })
    expect([options?.props.flexDirection, options?.props.flexWrap]).toEqual(['row', 'wrap'])
    expect((options?.children as { props: { key?: string } }[]).map(c => c.props.key)).toEqual(['local_a:q0:o0:pick', 'local_a:q0:o1:pick'])
    const pick = async (oi: number) => {
      const { label, variant } = (await ui.find({ key: `local_a:q0:o${oi}:pick` }))?.props ?? {}
      return [label, variant]
    }
    expect(await pick(0)).toEqual(['Postgres', 'primary'])
    expect(await pick(1)).toEqual(['Mongo', undefined])
    // An option without a description is left out of the caption
    const about = await ui.find({ type: 'Text', text: /relational/ })
    expect([about?.text, about?.props.size, about?.props.color]).toEqual(['Postgres: relational', 'caption', 'subtle'])
    // The question replaces the status row that says it in one line; one press answers it, so no Send
    expect(await ui.find({ type: 'Text', text: /Postgres \/ Mongo/ })).toBeFalsy()
    expect(await ui.find({ key: 'local_a:answer' })).toBeFalsy()
    await ui.unmount()
  }
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
  // Its options gone, Open is the main button again
  expect((await ui.find({ key: 'local_a:open' }))?.props.variant).toBe('primary')
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
  // No options to press: Message… answers it, so the buttons keep their weight
  const { plain, dimColor } = (await ui.find({ key: 'local_a:message' }))?.props ?? {}
  expect([plain, dimColor]).toEqual([undefined, undefined])
  expect((await ui.find({ key: 'local_a:open' }))?.props.variant).toBe('primary')
  await ui.unmount()
})

test('an idle tile asking a question is open', async ($, on) => {
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
  await ui.press({ key: 'local_a:message' })
  await ui.input({ key: 'local_a:draft', text: 'use option B' })
  expect(toasts.at(-1)).toBe('message for Worker A not sent: blocked by a hook')
  expect((await ui.find({ key: 'local_a:draft' }))?.props.value).toBe('use option B')

  // Send, the person typing nothing more, sends it again
  refuse = false
  await ui.press({ key: 'local_a:send' })
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

test("a message not sent leaves another tile's open field alone, its text kept for its own", async ($, on) => {
  let release = () => {}
  on('prompt.submit', () => new Promise(resolve => (release = () => resolve({ drop: 'blocked by a hook' }))))
  await start($, on, ok([worker({ state: 'busy', status: 'working' }), worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'working' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
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

test('a working tile keeps its status row between tools, so its height holds', async ($, on) => {
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
  // Each tile's info ends in its reply lines
  expect((await textsOf(ui, 'local_b:info'))?.at(-1)).toBe('Pushed.')
  expect((await textsOf(ui, 'local_a:info'))?.slice(-2)).toEqual(['feature/a', 'See RESULT.md, shot, Foo and [a](b).'])
  // Dim, not italic: italic grey at that size reads as noise
  expect((await ui.find({ type: 'Text', text: 'Pushed.' }))?.props.italic).toBeUndefined()
  expect((await ui.findAll({ type: 'Text', text: /IDLE|WAITING/ })).map(t => t.text)).toEqual(['WAITING', 'IDLE'])
  await ui.unmount()
})

test('a reply line asking the question the tile shows is not said again', async ($, on) => {
  await start($, on, ok([{ ...asking({ toolUseId: 'tu1', questions: [DB], askedAt: 1, answerable: true }), lastLines: ['I need a pick.', '**Which DB?**'] }]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.findAll({ type: 'Text', text: /Which DB/ })).map(t => t.text)).toEqual(['Which DB?'])
  expect((await ui.find({ type: 'Text', text: /^I need a pick\.$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test("a waiting tile shows no verdict its own marker overrides, only a detail its status lacks", async ($, on) => {
  await start($, on, ok([
    worker({ status: 'waiting', statusText: 'CI on PR #80', verdict: 'review_ready', detail: 'PR #80 ready' }),
    worker({ id: 'local_b', title: 'Worker B', status: 'waiting', statusText: 'CI on PR #81', verdict: 'completed', detail: 'CI on PR #81' }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ type: 'Text', text: /review_ready|completed/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^PR #80 ready$/ }))?.text).toBeTruthy()
  expect((await ui.findAll({ type: 'Text', text: /CI on PR #81/ })).length).toBe(1)
  await ui.unmount()
})

test('the status line: the status cut to fit, then the model, its effort and the context use, never cut; the branch and PR on rows of their own under it', async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', statusText: 'working', model: 'opus', effort: 'medium', context: 16, branch: 'feature/board-test-pass-801d74', prs: [OPEN_PR] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const meta = await ui.find({ key: 'local_a:meta' })
  expect(meta?.props).toMatchObject({ flexDirection: 'row', minWidth: 0 })
  expect((meta?.children as { props: Record<string, unknown> }[]).map(c => c.props.flexShrink)).toEqual([1, 0])
  expect(meta?.text).toBe('working· opus·medium · 16%')
  expect((await ui.find({ type: 'Text', text: '· opus·medium · 16%' }))?.props.tabular).toBe(true)
  // The branch cut at its start, its unique end kept, in the mono face
  expect((await ui.find({ type: 'Text', text: 'feature/board-test-pass-801d74' }))?.props).toMatchObject({ mono: true, wrap: 'truncate-start' })
  const order = (await ui.findAll({})).map(el => el.key ?? el.text)
  expect(order.indexOf('local_a:meta')).toBeGreaterThan(-1)
  expect(order.indexOf('local_a:meta')).toBeLessThan(order.indexOf('feature/board-test-pass-801d74'))
  expect(order.indexOf('feature/board-test-pass-801d74')).toBeLessThan(order.indexOf(`local_a:${OPEN_PR.url}`))
  await ui.unmount()
})

test('the model line: effort missing, the model alone; no branch, no branch row; a question shown, the model and context alone', async ($, on) => {
  const ask = { toolUseId: 'tu1', askedAt: 1, answerable: true, questions: [{ header: '', question: 'Ship it?', multiSelect: false, options: [{ label: 'Yes', description: '' }] }] }
  await start($, on, ok([
    worker({ state: 'busy', status: 'working', statusText: 'Bash', model: 'haiku', effort: '', context: 9, branch: '' }),
    worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'needs you', model: 'claude-x-1', effort: 'high', context: null, branch: '', ask }),
    // A dev older than this mod: no model
    worker({ id: 'local_c', title: 'Worker C', state: 'busy', status: 'working', statusText: 'Read', context: 5, branch: '' }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ key: 'local_a:meta' }))?.text).toBe('Bash· haiku · 9%')
  expect(await ui.find({ type: 'Text', text: 'feature/a' })).toBeFalsy()
  expect((await ui.find({ key: 'local_b:meta' }))?.text).toBe('claude-x-1·high')
  expect((await ui.find({ key: 'local_c:meta' }))?.text).toBe('Read· 5%')
  await ui.unmount()
})

test('the fold and Tasks buttons are labelled by their arrows: the desktop draws a label in their place', async ($, on) => {
  await start($, on, ok([...WORKERS.slice(0, 2), worker({ id: 'local_c', title: 'Worker C', state: 'stopped', status: 'stopped', plan: [...PLAN] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ key: 'local_a:fold' }))?.props.label).toBe('▾')
  expect((await ui.find({ key: 'local_c:fold' }))?.props.label).toBe('▸')
  await unfold(ui, 'local_c')
  expect((await ui.find({ key: 'local_c:tasks' }))?.text).toBe('1/3 ▸')
  await ui.unmount()
})

test('a new question opens a tile the person folded; folded again, it stays folded for that question', async ($, on) => {
  let workers = [worker({ state: 'busy', status: 'working' })]
  const { clock } = await start($, on, () => ok(workers))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:fold' })
  expect((await ui.find({ key: 'local_a:fold' }))?.text).toBe('▸')

  const ask = { toolUseId: 'w1', questions: [FILE], askedAt: 0, answerable: false, reply: true }
  workers = [worker({ state: 'blocked', status: 'needs you', statusText: FILE.question, ask })]
  await clock.advance(2000)
  expect((await ui.find({ key: 'local_a:fold' }))?.text).toBe('▾')
  expect(await ui.find({ key: 'local_a:q0:o0:pick' })).toBeTruthy()

  await ui.press({ key: 'local_a:fold' })
  workers = [worker({ state: 'blocked', status: 'needs you', statusText: FILE.question, ask, since: '1m' })]
  await clock.advance(2000)
  expect((await ui.find({ key: 'local_a:fold' }))?.text).toBe('▸')
  expect(await header(ui)).toBe(`Worker A · ${FILE.question} · 1m ⚠`)

  // Another worker's new question opens only its own tile
  workers = [worker({ state: 'blocked', status: 'needs you', statusText: FILE.question, ask }), worker({ id: 'local_b', title: 'Worker B', state: 'busy', status: 'working' })]
  await clock.advance(2000)
  await ui.press({ key: 'local_b:fold' })
  workers = [workers[0], worker({ id: 'local_b', title: 'Worker B', state: 'blocked', status: 'needs you', statusText: 'Really?', ask: { ...ask, toolUseId: 'w9' } })]
  await clock.advance(2000)
  expect([(await ui.find({ key: 'local_a:fold' }))?.text, (await ui.find({ key: 'local_b:fold' }))?.text]).toEqual(['▸', '▾'])

  workers = [worker({ state: 'blocked', status: 'needs you', statusText: 'Really?', ask: { ...ask, toolUseId: 'w2' } })]
  await clock.advance(2000)
  expect((await ui.find({ key: 'local_a:fold' }))?.text).toBe('▾')
  await ui.unmount()
})

test('a failed poll does not make a pending question new: its tile stays folded', async ($, on) => {
  const ask = { toolUseId: 'w1', questions: [FILE], askedAt: 0, answerable: false, reply: true }
  const asking = ok([worker({ state: 'blocked', status: 'needs you', statusText: FILE.question, ask })])
  let ran: Ran = asking
  const { clock } = await start($, on, () => ran)
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await ui.press({ key: 'local_a:fold' })
  ran = { exitCode: 1, stdout: '', stderr: 'boom' }
  await clock.advance(2000)
  ran = asking
  await clock.advance(2000)
  expect((await ui.find({ key: 'local_a:fold' }))?.text).toBe('▸')
  await ui.unmount()
})

test('a named worker is titled by its name, its long title a caption under it; folded, the name alone', async ($, on) => {
  await start($, on, ok([
    worker({ name: 'fix-login', title: 'Fix the login redirect loop', status: 'working', state: 'busy' }),
    worker({ id: 'local_b', name: 'docs', title: 'docs', status: 'working', state: 'busy' }),
    worker({ id: 'local_c', name: 'spike', title: 'An abandoned spike', status: 'idle' }),
  ]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await header(ui)).toBe('fix-login')
  expect((await textsOf(ui, 'local_a:head'))?.at(-1)).toBe('Fix the login redirect loop')
  // A title that is the name says nothing more
  expect(await textsOf(ui, 'local_b:head')).toEqual(['docs'])
  expect(await header(ui, 'local_c')).toBe('spike')
  expect(await ui.find({ type: 'Text', text: 'An abandoned spike' })).toBeFalsy()
  await ui.unmount()
})

test('colour marks what asks for attention: Needs you and Waiting cards in theirs, Working and Idle grey', async ($, on) => {
  await start($, on, ok(BY_STATUS.map(w => ({ ...w, statusText: 'on it' }))))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  await unfold(ui, 'local_i')
  const card = async (id: string) => {
    const tile = (await ui.findAll({ type: 'Box' })).find(b => b.props.borderStyle === 'quote' && JSON.stringify(b.children).includes(`${id}:rows`))
    return [tile?.props.borderColor, tile?.props.borderDimColor]
  }
  expect(await Promise.all(['local_n', 'local_w', 'local_k', 'local_i'].map(card))).toEqual([
    ['error', false],
    ['warning', false],
    ['inactive', true],
    ['inactive', true],
  ])
  // The status row, first in its info: a caption under the title, red under Needs you, else muted
  const status = async (id: string) => {
    const row = (await ui.find({ key: `${id}:meta` }))?.children as { children: { props: Record<string, unknown> }[] }[] | undefined
    const text = row?.[0]?.children[0]?.props
    return [text?.size, text?.color, text?.weight]
  }
  // The dot says the section, not the app's state: a busy worker under Needs you is red; a stopped one inactive
  await unfold(ui, 'local_s')
  const dot = async (id: string) => {
    const tile = (await ui.findAll({ type: 'Box' })).find(b => b.props.borderStyle === 'quote' && JSON.stringify(b.children).includes(`${id}:rows`))
    return (tile?.children[1] as { props: Record<string, unknown> } | undefined)?.props.color
  }
  expect(await Promise.all(['local_n', 'local_w', 'local_b', 'local_k', 'local_t', 'local_i', 'local_s'].map(dot))).toEqual(['error', 'warning', 'warning', 'success', 'success', 'subtle', 'inactive'])
  expect(await Promise.all(['local_n', 'local_w', 'local_k', 'local_i'].map(status))).toEqual([
    ['caption', 'error', 'semibold'],
    ['caption', 'subtle', 'regular'],
    ['caption', 'subtle', 'regular'],
    ['caption', 'subtle', 'regular'],
  ])
  await ui.unmount()
})

test("an open tile is blocks one gap apart: title, what it does, tasks, buttons; no margins of their own", async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', status: 'working', statusText: 'Bash', plan: [...PLAN] })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const rows = await ui.find({ key: 'local_a:rows' })
  expect(rows?.props.rowGap).toBe('xs')
  expect((rows?.children as { props: { key?: string } }[]).map(c => c.props.key)).toEqual(['local_a:head', 'local_a:info', 'local_a:plan', 'local_a:actions'])
  expect((await ui.findAll({ type: 'Box' })).filter(b => b.props.marginTop !== undefined)).toEqual([])
  await ui.unmount()
})

test('a question is a block of its own, between the title and what the tile does', async ($, on) => {
  const ask = { toolUseId: 'tu1', askedAt: 1, answerable: true, questions: [{ header: '', question: 'Ship it?', multiSelect: false, options: [{ label: 'Yes', description: '' }] }] }
  await start($, on, ok([worker({ status: 'needs you', ask })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const rows = await ui.find({ key: 'local_a:rows' })
  expect((rows?.children as { props: { key?: string } }[]).map(c => c.props.key)).toEqual(['local_a:head', 'local_a:ask', 'local_a:info', 'local_a:actions'])
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

test('folded or open, a tile is the same card, its arrow and dot first, so unfolding only opens its body', async ($, on) => {
  await start($, on, ok([worker({ status: 'idle' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  const card = async () => {
    const tile = (await ui.findAll({ type: 'Box' })).find(b => b.props.borderStyle === 'quote')
    return [tile?.props.paddingLeft, (tile?.children as { type: string; props: { key?: string } }[]).map(c => c.props.key ?? c.type)]
  }
  expect(await card()).toEqual([1, ['local_a:fold', 'Text', 'local_a:header']])
  await ui.press({ key: 'local_a:fold' })
  expect(await card()).toEqual([1, ['local_a:fold', 'Text', 'local_a:rows']])
  await ui.unmount()
})

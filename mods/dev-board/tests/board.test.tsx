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
  ...fields,
})

const WORKERS = [
  worker({ state: 'busy', tool: 'Bash', context: 42, lastLines: ['Running the tests.'], verdict: 'completed' }),
  worker({
    id: 'local_b',
    title: 'Worker B',
    state: 'blocked',
    verdict: 'blocked',
    detail: 'two ways',
    needsAction: 'pick one',
    prs: [{ number: 78, url: 'https://github.com/o/r/pull/78', state: 'OPEN' }],
  }),
  worker({ id: 'local_c', title: 'Worker C', state: 'stopped' }),
]

type Ran = { exitCode: number; stdout: string; stderr: string }

/** Starts the session with dev answering `answer()`'s run, or failing to start with that reason. */
async function start($: Engine, on: On, ran: Ran | string | (() => Ran | string)) {
  const answer = typeof ran === 'function' ? ran : () => ran
  const runs: (readonly string[])[] = []
  on('command.register', () => ({ value: { command: 'board' } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('process.run', (_, e) => {
    runs.push(e.argv)
    const now = answer()
    if (typeof now === 'string') {
      return { deny: now }
    }
    return { value: { ...now, isStdoutTruncated: false, isStderrTruncated: false } }
  })
  mock.env(on, { CLAUDE_CODE_HOST_SESSION_ID: 'local_boss' })
  const clock = mock.clock(on)
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await clock.settle()
  return { runs, clock }
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
    expect((await ui.find({ type: 'Text', text: /running Bash/ }))?.text).toBe('running Bash')
    expect((await ui.find({ type: 'Text', text: /Running the tests/ }))?.text).toBe('Running the tests.')
    expect((await ui.find({ type: 'Text', text: /last turn: completed/ }))?.text).toBe('last turn: completed')
    expect((await ui.find({ type: 'Text', text: /^needs:/ }))?.text).toBe('needs: pick one')
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

test("a busy worker hides its last turn's needs", async ($, on) => {
  await start($, on, ok([worker({ state: 'busy', verdict: 'blocked', needsAction: 'old ask' })]))
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect(await ui.find({ type: 'Text', text: /needs: old ask/ })).toBeFalsy()
  expect((await ui.find({ type: 'Text', text: /^last turn: blocked$/ }))?.text).toBeTruthy()
  await ui.unmount()
})

test('output this mod does not read says so', async ($, on) => {
  await start($, on, { exitCode: 0, stdout: JSON.stringify([{ id: 'x' }]), stderr: '' })
  const ui = await $.ui.mount({ ...PANE, surface: 'desktop' })
  expect((await ui.find({ type: 'Text' }))?.text).toMatch(/is dev up to date/)
  await ui.unmount()
})

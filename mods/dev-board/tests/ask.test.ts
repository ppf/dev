import type { On } from 'claude-code'
import { expect, mock, test } from 'claude-code/testing'

const DIR = '/state/dev/desktop/questions'
const QUESTIONS = [
  { question: 'Which DB?', header: 'DB', multiSelect: false, options: [{ label: 'Postgres', description: 'relational' }, { label: 'Mongo', description: 'documents' }] },
]

/** A disk in memory under $.fs and the mv and rm the hook runs (an mv failing with `mvFails`); returns its files and every argv run. */
function disk(on: On, { mvFails = false } = {}) {
  const files = new Map<string, string>()
  const runs: (readonly string[])[] = []
  on('fs.write', (_, e) => {
    files.set(e.path, e.text)
    return { value: undefined }
  })
  on('fs.exists', (_, e) => ({ value: files.has(e.path) }))
  on('fs.read', (_, e) => {
    const text = files.get(e.path)
    if (text === undefined) throw new Error(`ENOENT: ${e.path}`)
    return { value: text }
  })
  on('process.run', (_, e) => {
    runs.push(e.argv)
    const [cmd, , ...paths] = e.argv
    if (cmd === 'mv' && mvFails) {
      return { value: { exitCode: 1, stdout: '', stderr: 'denied', isStdoutTruncated: false, isStderrTruncated: false } }
    }
    if (cmd === 'mv') {
      const [from, to] = paths
      files.set(to ?? '', files.get(from ?? '') ?? '')
      files.delete(from ?? '')
    }
    if (cmd === 'rm') for (const p of paths) files.delete(p)
    return { value: { exitCode: 0, stdout: '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  return { files, runs }
}

/** The engine's dialog beneath: open until `answer` is called; `aborted` once the call is abandoned. */
function dialog(on: On) {
  const d = { aborted: false, answer: (_: string) => {} }
  on('tool.call', (_, __, next) => {
    next.signal.addEventListener('abort', () => (d.aborted = true))
    return new Promise(resolve => (d.answer = label => resolve({ result: { questions: QUESTIONS, answers: { 'Which DB?': label } } })))
  })
  return d
}

const env = (on: On) => mock.env(on, { CLAUDE_CODE_HOST_SESSION_ID: 'local_w', XDG_STATE_HOME: '/state' })

test('an open question is written whole for the board, then the board answer for it answers it and the dialog is abandoned', async ($, on) => {
  env(on)
  const clock = mock.clock(on)
  const { files, runs } = disk(on)
  const d = dialog(on)

  const call = $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'tu1', questions: QUESTIONS })
  await clock.settle()
  // Written beside it and moved in place
  expect(runs[0]).toEqual(['mv', '-f', `${DIR}/local_w.json.tmp`, `${DIR}/local_w.json`])
  const asked = JSON.parse(files.get(`${DIR}/local_w.json`) ?? 'null')
  expect(asked).toEqual({ session: 'local_w', toolUseId: 'tu1', questions: QUESTIONS, askedAt: 0 })

  // An answer to an earlier question is not this one's
  files.set(`${DIR}/local_w.answer.json`, JSON.stringify({ toolUseId: 'tu0', answers: { 'Which DB?': 'Postgres' } }))
  await clock.advance(3000)
  expect(d.aborted).toBe(false)

  files.set(`${DIR}/local_w.answer.json`, JSON.stringify({ toolUseId: 'tu1', answers: { 'Which DB?': 'Mongo' } }))
  await clock.advance(1000)
  const got = await call

  expect(got.result).toEqual({ questions: QUESTIONS, answers: { 'Which DB?': 'Mongo' } })
  expect(d.aborted).toBe(true)
  expect([...files.keys()]).toEqual([])
})

test('a pick in the dialog answers it, and the files go', async ($, on) => {
  env(on)
  const clock = mock.clock(on)
  const { files } = disk(on)
  const d = dialog(on)

  const call = $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'tu2', questions: QUESTIONS })
  await clock.advance(30_000)
  d.answer('Postgres')

  expect((await call).result).toEqual({ questions: QUESTIONS, answers: { 'Which DB?': 'Postgres' } })
  expect([...files.keys()]).toEqual([])
})

test('outside a desktop session only the dialog answers', async ($, on) => {
  mock.env(on, { XDG_STATE_HOME: '/state' })
  mock.clock(on)
  const { files } = disk(on)
  on('tool.call', () => ({ result: { questions: QUESTIONS, answers: { 'Which DB?': 'Mongo' } } }))

  expect((await $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'tu3', questions: QUESTIONS })).result).toBeTruthy()
  expect([...files.keys()]).toEqual([])
})

test('a call with no tool use id: only the dialog answers', async ($, on) => {
  env(on)
  mock.clock(on)
  const { files } = disk(on)
  on('tool.call', () => ({ result: { questions: QUESTIONS, answers: { 'Which DB?': 'Mongo' } } }))

  expect((await $.tool.call({ tool: 'AskUserQuestion', questions: QUESTIONS })).result).toBeTruthy()
  expect([...files.keys()]).toEqual([])
})

test('the board answer still counts long after the hook started', async ($, on) => {
  env(on)
  const clock = mock.clock(on)
  const { files } = disk(on)
  const d = dialog(on)

  const call = $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'tu5', questions: QUESTIONS })
  await clock.advance(5 * 60_000)
  files.set(`${DIR}/local_w.answer.json`, JSON.stringify({ toolUseId: 'tu5', answers: { 'Which DB?': 'Mongo' } }))
  await clock.advance(1000)

  expect((await call).result).toEqual({ questions: QUESTIONS, answers: { 'Which DB?': 'Mongo' } })
  expect(d.aborted).toBe(true)
})

test('a dismissed dialog takes the question files with it', async ($, on) => {
  env(on)
  const clock = mock.clock(on)
  const { files } = disk(on)
  let dismiss: (err: Error) => void = () => {}
  on('tool.call', () => new Promise((_, reject) => (dismiss = reject)))

  const call = $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'tu6', questions: QUESTIONS }).then(
    r => r,
    (err: unknown) => err,
  )
  await clock.settle()
  expect([...files.keys()]).toEqual([`${DIR}/local_w.json`])
  dismiss(new Error('dismissed'))
  await call

  expect([...files.keys()]).toEqual([])
})

test('a question file that cannot be moved in place leaves the dialog alone to answer', async ($, on) => {
  env(on)
  mock.clock(on)
  const { files } = disk(on, { mvFails: true })
  on('tool.call', () => ({ result: { questions: QUESTIONS, answers: { 'Which DB?': 'Postgres' } } }))

  const got = await $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'tu7', questions: QUESTIONS })
  expect(got.result).toEqual({ questions: QUESTIONS, answers: { 'Which DB?': 'Postgres' } })
  expect([...files.keys()]).toEqual([])
})

test('the dialog still asks when the question cannot be written', async ($, on) => {
  env(on)
  mock.clock(on)
  on('fs.write', () => {
    throw new Error('EACCES')
  })
  on('tool.call', () => ({ result: { questions: QUESTIONS, answers: { 'Which DB?': 'Mongo' } } }))

  const got = await $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'tu4', questions: QUESTIONS })
  expect(got.result).toEqual({ questions: QUESTIONS, answers: { 'Which DB?': 'Mongo' } })
})

test("a subagent's question keeps to its own dialog", async ($, on) => {
  env(on)
  mock.clock(on)
  const { files } = disk(on)
  on('tool.call', () => ({ result: { questions: QUESTIONS, answers: { 'Which DB?': 'Mongo' } } }))

  expect((await $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'tu8', agentId: 'a1', questions: QUESTIONS })).result).toBeTruthy()
  expect([...files.keys()]).toEqual([])
})

test("a call's cleanup leaves a question asked since in place", async ($, on) => {
  env(on)
  const clock = mock.clock(on)
  const { files } = disk(on)
  const d = dialog(on)

  const call = $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'tu9', questions: QUESTIONS })
  await clock.settle()
  const newer = JSON.stringify({ session: 'local_w', toolUseId: 'tu10', questions: QUESTIONS, askedAt: 1 })
  files.set(`${DIR}/local_w.json`, newer)
  d.answer('Postgres')
  await call

  expect(files.get(`${DIR}/local_w.json`)).toBe(newer)
})

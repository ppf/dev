import type { On } from 'claude-code'
import { expect, mock, test } from 'claude-code/testing'
import type { Engine } from 'claude-code/testing'

const DIR = '/state/dev/desktop/questions'
const LISTEN = `${DIR}/local_w.listen`
const REPLY = `${DIR}/local_w.reply.json`
const WRAP_UP = 'mcp__ccd_turn__wrap_up'

/** A worker session started on a disk in memory (the board's `dev desktop board` failing; `removing` runs as a file is removed); its files, prompts and toasts, and the clock. */
async function worker($: Engine, on: On, { submitted = (): { drop: string } | undefined => undefined, removing = async () => {} } = {}) {
  const files = new Map<string, string>()
  const prompts: { text: string; asUser: boolean }[] = []
  const toasts: string[] = []
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
  on('process.run', async (_, e) => {
    const [cmd, , ...paths] = e.argv
    if (cmd === 'rm') await removing()
    if (cmd === 'mv') {
      const [from, to] = paths
      files.set(to ?? '', files.get(from ?? '') ?? '')
      files.delete(from ?? '')
    }
    if (cmd === 'rm') for (const p of paths) files.delete(p)
    return { value: { exitCode: cmd === 'dev' ? 1 : 0, stdout: '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('prompt.submit', (_, e) => {
    prompts.push({ text: e.text, asUser: e.origin.kind === 'plugin' && e.origin.asUser === true })
    return submitted() ?? { text: e.text, turnId: 't' }
  })
  on('ui.toast', (_, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('command.register', () => ({ value: { command: 'board' } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('turn.start', (_, e) => ({ turnId: e.turnId }))
  on('tool.call', () => ({ result: 'ok' }))
  mock.env(on, { CLAUDE_CODE_HOST_SESSION_ID: 'local_w', XDG_STATE_HOME: '/state' })
  const clock = mock.clock(on)
  await $.session.start({ cwd: '/', surface: 'desktop', isInteractive: true })
  await clock.settle()
  return { files, prompts, toasts, clock }
}

/** The board's reply, as writeReply writes it. */
const reply = (toolUseId: string, text: string, writtenAt = 0) => JSON.stringify({ toolUseId, text, writtenAt })

/** A main-loop turn that ends on a wrap_up ask (tool use `id`). */
async function wrapUp($: Engine, id: string, agentId?: string) {
  await $.turn.start({ text: 'go', turnId: `turn-${id}`, ...(agentId === undefined ? {} : { agentId }) })
  await $.tool.call({ tool: WRAP_UP, tool_use_id: id, recap: 'done', ask: 'Which?', ...(agentId === undefined ? {} : { agentId }) })
}

test('a running session writes its heartbeat every 2 s', async ($, on) => {
  const { files, clock } = await worker($, on)
  await clock.advance(1000)
  expect(files.get(LISTEN)).toBe('1000')
  await clock.advance(1000)
  expect(files.get(LISTEN)).toBe('1000')
  await clock.advance(1000)
  expect(files.get(LISTEN)).toBe('3000')
})

test("the board's reply to the wrap_up the turn ended on is submitted once, as the person's own", async ($, on) => {
  const { files, prompts, clock } = await worker($, on)
  await wrapUp($, 'w1')
  files.set(REPLY, reply('w1', 'Yes, fix 1'))
  await clock.advance(1000)
  expect(prompts).toEqual([{ text: 'Yes, fix 1', asUser: true }])
  expect(files.has(REPLY)).toBe(false)

  // A second press for the same ask is no answer any more
  files.set(REPLY, reply('w1', 'Not now', 1000))
  await clock.advance(1000)
  expect(prompts.length).toBe(1)
  expect(files.has(REPLY)).toBe(false)
})

test('a reply to another wrap_up, one older than a minute, or one after a turn started is dropped', async ($, on) => {
  const { files, prompts, clock } = await worker($, on)
  await wrapUp($, 'w2')
  files.set(REPLY, reply('w1', 'Yes'))
  await clock.advance(1000)
  expect(files.has(REPLY)).toBe(false)

  await clock.advance(60_000)
  files.set(REPLY, reply('w2', 'Yes', 0))
  await clock.advance(1000)
  expect(files.has(REPLY)).toBe(false)

  await wrapUp($, 'w3')
  await $.turn.start({ text: 'typed', turnId: 'turn-typed' })
  files.set(REPLY, reply('w3', 'Yes', 62_000))
  await clock.advance(1000)
  expect(files.has(REPLY)).toBe(false)
  expect(prompts).toEqual([])
})

test("a subagent's wrap_up is no ask the board's reply answers", async ($, on) => {
  const { files, prompts, clock } = await worker($, on)
  await wrapUp($, 'w1')
  await $.tool.call({ tool: WRAP_UP, tool_use_id: 'w9', recap: 'sub', ask: 'Sub?', agentId: 'a1' })
  files.set(REPLY, reply('w9', 'Yes'))
  await clock.advance(1000)
  expect(prompts).toEqual([])
  files.set(REPLY, reply('w1', 'Yes', 1000))
  await clock.advance(1000)
  expect(prompts).toEqual([{ text: 'Yes', asUser: true }])
})

test('a reply the app refuses says so in a toast', async ($, on) => {
  const { files, toasts, clock } = await worker($, on, { submitted: () => ({ drop: 'blocked by a hook' }) })
  await wrapUp($, 'w1')
  files.set(REPLY, reply('w1', 'Yes'))
  await clock.advance(1000)
  expect(toasts).toEqual(['dev-board: the board\'s reply "Yes" not sent: blocked by a hook'])
})

test('a reply taken as a turn starts is not submitted behind it', async ($, on) => {
  let typed = false
  const { files, prompts, clock } = await worker($, on, {
    removing: async () => {
      if (typed) await $.turn.start({ text: 'typed', turnId: 'turn-typed' })
    },
  })
  await wrapUp($, 'w1')
  typed = true
  files.set(REPLY, reply('w1', 'Yes'))
  await clock.advance(1000)
  expect(files.has(REPLY)).toBe(false)
  expect(prompts).toEqual([])
})

test('a broken reply file is dropped and the next one still taken', async ($, on) => {
  const { files, prompts, clock } = await worker($, on)
  await wrapUp($, 'w1')
  files.set(REPLY, '{')
  await clock.advance(1000)
  expect(files.has(REPLY)).toBe(false)
  files.set(REPLY, reply('w1', 'Yes', 1000))
  await clock.advance(1000)
  expect(prompts).toEqual([{ text: 'Yes', asUser: true }])
})

test('after a refused reply the ask still takes the next press', async ($, on) => {
  let refuse = true
  const { files, prompts, clock } = await worker($, on, { submitted: () => (refuse ? { drop: 'busy' } : undefined) })
  await wrapUp($, 'w1')
  files.set(REPLY, reply('w1', 'Yes'))
  await clock.advance(1000)
  refuse = false
  files.set(REPLY, reply('w1', 'Yes', 1000))
  await clock.advance(1000)
  expect(prompts).toEqual([
    { text: 'Yes', asUser: true },
    { text: 'Yes', asUser: true },
  ])
})

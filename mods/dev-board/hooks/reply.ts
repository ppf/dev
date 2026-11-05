import { atom, read, update } from 'claude-code'
import type { EngineInterface, On } from 'claude-code'

import { errorText, questionsDir } from './ask'

const WRAP_UP = 'mcp__ccd_turn__wrap_up'
const POLL_MS = 1000
// The board takes a heartbeat older than 10 s (`listenFresh`) as no listener
const BEAT_MS = 2000
// A reply the board wrote longer ago than this is no answer to what the worker shows now
const REPLY_TTL_MS = 60_000

type Reply = { toolUseId: string; text: string; writtenAt: number }

// The wrap_up the main loop's last turn ended on, '' once a turn starts: the one reply the board's reply file may answer
const replyTo = atom({ plugin: 'dev-board', key: 'replyTo' } as const, '', { shape: 'reply-to-1' })

/** Writes the board's reply to worker id's wrap_up ask, for its mod to submit as the person's next prompt; why not, or ''. */
export async function writeReply($: EngineInterface, id: string, toolUseId: string, text: string): Promise<string> {
  const path = `${await questionsDir($)}/${id}.reply.json`
  try {
    // Written whole and moved in place, so the worker never reads half of it
    await $.fs.write(`${path}.tmp`, JSON.stringify({ toolUseId, text, writtenAt: await $.clock.now() } satisfies Reply))
    const moved = await $.process.run(['mv', '-f', `${path}.tmp`, path])
    if (moved.exitCode === 0) return ''
    await $.process.run(['rm', '-f', `${path}.tmp`])
    return moved.stderr.trim() || `mv exited ${moved.exitCode}`
  } catch (err) {
    return errorText(err)
  }
}

// Turns started on the main loop, so a reply taken as another turn starts is not submitted behind it
let turns = 0

function log($: EngineInterface, text: string) {
  return $.ui.log(`dev-board: ${text}`, { to: 'debug' })
}

/** Keeps the wrap_up the main loop's last turn ended on, for listenForReplies. */
export function registerReply(on: On): void {
  on('turn.start', async ($, e, next) => {
    if (e.agentId === undefined) {
      turns++
      await update($, replyTo, () => '')
    }
    return next(e)
  }).catch(($, e, next) => {
    log($, `reply target not cleared: ${next.error.message ?? next.error.kind}`)
    return next(e)
  })

  // A failed hook would fail the wrap_up call itself: the catch lets it through
  on('tool.call', { tool: WRAP_UP }, async ($, e, next) => {
    if (e.agentId === undefined) await update($, replyTo, () => e.tool_use_id)
    return next(e)
  }).catch(($, e, next) => {
    log($, `wrap_up not kept for replies: ${next.error.message ?? next.error.kind}`)
    return next(e)
  })
}

/** Rewrites questions/<session>.listen (the wrap_up it answers) every BEAT_MS and submits the board's reply to the last turn's wrap_up ask as the person's own, once; any other is dropped. */
export async function listenForReplies($: EngineInterface, session: string): Promise<void> {
  const dir = await questionsDir($)
  const listen = `${dir}/${session}.listen`
  const replied = `${dir}/${session}.reply.json`
  let beatAt = -Infinity
  let beatTarget = ''
  let isReading = false
  let lastError = ''
  const say = (text: string) => {
    // Said once per error, not every period
    if (text !== lastError) log($, (lastError = text))
  }
  $.clock.every(POLL_MS, async () => {
    if (isReading) return
    isReading = true
    const turn = turns
    try {
      const now = await $.clock.now()
      // Names the wrap_up a reply may answer, so the board routes none here for an ask this copy never saw
      const target = await read($, replyTo)
      if (now - beatAt >= BEAT_MS || target !== beatTarget) {
        await $.fs.write(listen, target).catch((err: unknown) => say(`heartbeat not written: ${errorText(err)}`))
        beatAt = now
        beatTarget = target
      }
      if (!(await $.fs.exists(replied))) return
      const text = await $.fs.read(replied)
      // Gone before it is read for what it says: a reply is taken once, or dropped
      const removed = await $.process.run(['rm', '-f', replied])
      if (removed.exitCode !== 0) return say(`reply file not removed, not taken: ${removed.stderr.trim()}`)
      const r = JSON.parse(text) as Partial<Reply>
      const to = await read($, replyTo)
      if (to === '' || r.toolUseId !== to || typeof r.text !== 'string' || r.text === '' || typeof r.writtenAt !== 'number' || now - r.writtenAt > REPLY_TTL_MS) {
        return log($, `reply for ${String(r.toolUseId)} dropped: this session's last turn ended on ${to || 'no wrap_up'}`)
      }
      await update($, replyTo, () => '')
      const reply = r.text
      // Sent from a timer of its own, as /board's prompt is
      $.clock.after(0, async () => {
        if (turns !== turn) return log($, `reply for ${to} dropped: a turn started before it was sent`)
        const sent = await $.prompt.submit({ text: reply, asUser: true }).catch((err: unknown) => ({ drop: errorText(err) }))
        if (sent.drop === undefined) return
        // Still the ask the worker shows: the board offers it again
        if (turns === turn) await update($, replyTo, () => to)
        await $.ui.toast(`dev-board: the board's reply "${reply}" not sent: ${sent.drop}`)
      })
      lastError = ''
    } catch (err) {
      say(`reply not read: ${errorText(err)}`)
    } finally {
      isReading = false
    }
  })
}

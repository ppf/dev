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
    return moved.exitCode === 0 ? '' : moved.stderr.trim() || `mv exited ${moved.exitCode}`
  } catch (err) {
    return errorText(err)
  }
}

// Turns started on the main loop, so a reply taken just before another turn starts is not submitted behind it
let turns = 0

/** Keeps the wrap_up the main loop's last turn ended on, for listenForReplies. */
export function registerReply(on: On): void {
  on('turn.start', async ($, e, next) => {
    if (e.agentId === undefined) {
      turns++
      await update($, replyTo, () => '').catch((err: unknown) => $.ui.log(`dev-board: reply target not cleared: ${errorText(err)}`, { to: 'debug' }))
    }
    return next(e)
  })

  // Never throws: a failed hook would fail the wrap_up call itself
  on('tool.call', { tool: WRAP_UP }, async ($, e, next) => {
    if (e.agentId === undefined) {
      await update($, replyTo, () => e.tool_use_id).catch((err: unknown) => $.ui.log(`dev-board: wrap_up not kept for replies: ${errorText(err)}`, { to: 'debug' }))
    }
    return next(e)
  })
}

/**
 * Takes the board's reply to app session `session`'s wrap_up ask as the person's own: writes
 * questions/<session>.listen every BEAT_MS, and submits questions/<session>.reply.json's text once, if it
 * answers the wrap_up the last turn ended on; any other is dropped.
 */
export async function listenForReplies($: EngineInterface, session: string): Promise<void> {
  const dir = await questionsDir($)
  const listen = `${dir}/${session}.listen`
  const replied = `${dir}/${session}.reply.json`
  let beatAt = -Infinity
  let isReading = false
  let lastError = ''
  const say = (text: string) => {
    // Said once per error, not every period
    if (text !== lastError) $.ui.log(`dev-board: ${(lastError = text)}`, { to: 'debug' })
  }
  $.clock.every(POLL_MS, async () => {
    if (isReading) return
    isReading = true
    try {
      const now = await $.clock.now()
      if (now - beatAt >= BEAT_MS) {
        await $.fs.write(listen, String(now))
        beatAt = now
      }
      if (!(await $.fs.exists(replied))) return
      const text = await $.fs.read(replied)
      // Gone before it is read for what it says: a reply is taken once, or dropped
      const removed = await $.process.run(['rm', '-f', replied])
      if (removed.exitCode !== 0) return say(`reply file not removed, not taken: ${removed.stderr.trim()}`)
      const r = JSON.parse(text) as Partial<Reply>
      const to = await read($, replyTo)
      if (to === '' || r.toolUseId !== to || typeof r.text !== 'string' || r.text === '' || typeof r.writtenAt !== 'number' || now - r.writtenAt > REPLY_TTL_MS) {
        return say(`reply for ${String(r.toolUseId)} dropped: this session's last turn ended on ${to || 'no wrap_up'}`)
      }
      await update($, replyTo, () => '')
      const turn = turns
      // Sent from a timer of its own, as /board's prompt is
      $.clock.after(0, async () => {
        if (turns !== turn) return say(`reply for ${to} dropped: a turn started before it was sent`)
        const sent = await $.prompt.submit({ text: r.text ?? '', asUser: true }).catch((err: unknown) => ({ drop: errorText(err) }))
        if (sent.drop !== undefined) await $.ui.toast(`dev-board: the board's reply "${r.text}" not sent: ${sent.drop}`)
      })
    } catch (err) {
      say(`reply not read: ${errorText(err)}`)
    } finally {
      isReading = false
    }
  })
}

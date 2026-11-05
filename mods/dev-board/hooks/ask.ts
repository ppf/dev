import type { EngineInterface, On } from 'claude-code'

// How often a worker's open question looks for the board's answer
const ANSWER_POLL_MS = 1000

type Answer = { toolUseId: string; answers: Record<string, string> }

const errorText = (err: unknown) => (err instanceof Error ? err.message : String(err))

/** This session's question and answer files, under dev's state directory; undefined outside a desktop app session. */
async function files($: EngineInterface) {
  const session = await $.env.get('CLAUDE_CODE_HOST_SESSION_ID')
  if (!session) return undefined
  const state = (await $.env.get('XDG_STATE_HOME')) || `${await $.env.get('HOME')}/.local/state`
  const dir = `${state}/dev/desktop/questions`
  return { session, asked: `${dir}/${session}.json`, answered: `${dir}/${session}.answer.json` }
}

/** Removes those of the files that are this call's (another call's question may have replaced them), saying in the debug log when it can't. */
async function removeOwn($: EngineInterface, paths: string[], toolUseId: string) {
  const own: string[] = []
  for (const path of paths) {
    try {
      if ((await $.fs.exists(path)) && (JSON.parse(await $.fs.read(path)) as { toolUseId?: string }).toolUseId === toolUseId) own.push(path)
    } catch (err) {
      $.ui.log(`dev-board: ${path} not read: ${errorText(err)}`, { to: 'debug' })
    }
  }
  if (own.length === 0) return
  const ran = await $.process.run(['rm', '-f', ...own])
  if (ran.exitCode !== 0) $.ui.log(`dev-board: question files not removed: ${ran.stderr.trim()}`, { to: 'debug' })
}

/** While a worker's AskUserQuestion dialog is open, its question sits in questions/<app session id>.json and the board's `dev desktop answer` for that tool use answers it; a pick in the dialog wins too. */
export function registerAsk(on: On): void {
  on('tool.call', { tool: 'AskUserQuestion' }, async ($, e, next) => {
    const f = await files($)
    const toolUseId = e.tool_use_id
    // A subagent's question keeps to its own dialog: its loop would share the session's one question file
    if (f === undefined || !toolUseId || e.agentId !== undefined) return next(e)
    // Written whole and moved in place, so the board never reads half of it
    await $.fs.write(`${f.asked}.tmp`, JSON.stringify({ session: f.session, toolUseId, questions: e.questions, askedAt: await $.clock.now() }))
    const moved = await $.process.run(['mv', '-f', `${f.asked}.tmp`, f.asked])
    if (moved.exitCode !== 0) {
      await $.process.run(['rm', '-f', `${f.asked}.tmp`])
      throw new Error(`question file not written: ${moved.stderr.trim()}`)
    }

    // A timer, not a $.clock.sleep loop: a sleep is the hook's own time and spends its 10 s budget, while
    // awaiting with next(e) in flight spends none, however long the person takes
    let resolveBoard: (a: Answer) => void = () => {}
    const board = new Promise<Answer>(r => (resolveBoard = r))
    let isReading = false
    let lastError = ''
    const watch = $.clock.every(ANSWER_POLL_MS, async () => {
      if (isReading) return
      isReading = true
      try {
        if (!(await $.fs.exists(f.answered))) return
        const a = JSON.parse(await $.fs.read(f.answered)) as Partial<Answer>
        // An answer to an earlier question is not this one's
        if (a.toolUseId === toolUseId && typeof a.answers === 'object' && a.answers !== null) resolveBoard({ toolUseId, answers: a.answers })
      } catch (err) {
        // Said once per error, not every period
        if (errorText(err) !== lastError) $.ui.log(`dev-board: answer file not read: ${(lastError = errorText(err))}`, { to: 'debug' })
      } finally {
        isReading = false
      }
    })
    const fromDialog = next(e).then(r => ({ r }))
    try {
      const won = await Promise.race([fromDialog, board.then(a => ({ a }))])
      if ('r' in won) return won.r
      // The dialog beneath is abandoned: how it ends is no one's to handle but the log's
      fromDialog.catch((err: unknown) => $.ui.log(`dev-board: abandoned dialog ended: ${errorText(err)}`, { to: 'debug' }))
      return { result: { questions: e.questions, answers: won.a.answers } }
    } finally {
      watch.cancel()
      await removeOwn($, [f.asked, f.answered], toolUseId)
    }
  }).catch(async ($, e, next) => {
    $.ui.log(`dev-board: question hook failed, the dialog alone answers: ${next.error.message ?? next.error.kind}`, { to: 'debug' })
    // So the board offers no answer nothing reads
    const f = await files($)
    if (f !== undefined && e.tool_use_id) await removeOwn($, [f.asked], e.tool_use_id)
    return next(e)
  })
}

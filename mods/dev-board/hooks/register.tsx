import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { Ask, Board, Checks, Delivered, Picks, PR, Status, Step, Worker } from '../types'
import { registerAsk } from './ask'
import { listenForReplies, registerReply, writeReply } from './reply'

// The dev binary with `dev desktop board`: an absolute path when the app's PATH lacks it
const DEV_BIN = 'dev'

const PANE = 'board'
const PERIOD_MS = 2000
// PR checks and `dev delivered` (a git fetch, gh) are read at most this often per PR or worker
const REFRESH_MS = 60_000
const NEEDS_YOU = 'needs you'
const NUDGE = 'Continue where you left off'
const BOSS_PROMPT = "dev-board: /board opened — load the dev-board:boss skill and act as boss for this session's workers."
const STATE_COLORS = { busy: 'success', blocked: 'warning', idle: 'subtle', stopped: 'inactive' } as const
// The pane's sections, in order; an Idle tile is folded unless it asks for the person, until they (un)fold it
const GROUPS = ['Needs you', 'Working', 'Waiting', 'Idle'] as const
const GROUP_COLORS = { 'Needs you': 'error', Working: 'suggestion', Waiting: 'warning', Idle: 'inactive' } as const
const GROUP_OF: Record<Status, (typeof GROUPS)[number]> = {
  'needs you': 'Needs you',
  working: 'Working',
  stuck: 'Working',
  'rate-limited': 'Working',
  waiting: 'Waiting',
  blocked: 'Waiting',
  'review ready': 'Waiting',
  idle: 'Idle',
  stopped: 'Idle',
}
const MAX_LAST_LINES = 2
const PR_COLORS: Record<string, string> = { OPEN: 'success', MERGED: 'merged', CLOSED: 'error' }
// One glyph family so the marks line up: done recedes, the current step takes the section's color, pending stays quiet
const STEP_LOOKS = {
  completed: { mark: '●', markColor: 'success', color: 'inactive', weight: 'regular', isDim: true },
  in_progress: { mark: '◐', markColor: undefined, color: 'text', weight: 'medium', isDim: false },
  pending: { mark: '○', markColor: 'subtle', color: 'subtle', weight: 'regular', isDim: false },
} as const
// An expanded list's rows at most, its count rows included; the progress bar's width in cells
const MAX_STEPS = 7
const BAR_CELLS = 10
const CHECK_ICONS: Record<string, string> = { pass: '✅', fail: '❌', pending: '⏳', skipping: '⏭', cancel: '⊘' }

type Run = (argv: string[], init?: { timeoutMs?: number; stdin?: string }) => Promise<{ exitCode: number; stdout: string; stderr: string }>

// Change the tag when Board's, Worker's, PR's, Note's, Step's, Ask's or Question's fields change: the atom then declines what an earlier version stored
const board = atom({ plugin: 'dev-board', key: 'board' } as const, null, { shape: 'board-6' })
// The keys of the notes already sent to the boss, newest last, so a reload sends none again
const sent = atom({ plugin: 'dev-board', key: 'sent' } as const, [], { shape: 'sent-1' })
const MAX_SENT = 500
// By PR URL
const checks = atom({ plugin: 'dev-board', key: 'checks' } as const, {}, { shape: 'checks-1' })
// By worker id
const delivered = atom({ plugin: 'dev-board', key: 'delivered' } as const, {}, { shape: 'delivered-2' })
const view = atom({ plugin: 'dev-board', key: 'view' } as const, { expanded: [], composing: '', tiles: {}, unsent: {} }, { shape: 'view-3' })
// Whether /board asked this session to be the boss, so it asks once per session
const bossAsked = atom({ plugin: 'dev-board', key: 'bossAsked' } as const, false, { shape: 'boss-asked-1' })
// The person's picks on the workers' questions, by worker id
const picks = atom({ plugin: 'dev-board', key: 'picks' } as const, {}, { shape: 'picks-1' })

const errorText = (err: unknown) => (err instanceof Error ? err.message : String(err))

/** Runs `dev desktop board` once: its workers with its stderr as warnings, or its error text. */
async function poll(run: Run, boss: string): Promise<Board> {
  try {
    const ran = await run([DEV_BIN, 'desktop', 'board', '--json', '--boss', boss])
    const stderr = ran.stderr.trim()
    if (ran.exitCode !== 0) {
      return { workers: [], error: stderr || `${DEV_BIN} desktop board exited ${ran.exitCode}`, warnings: '' }
    }
    const workers: unknown = JSON.parse(ran.stdout)
    // A dev older or newer than this mod: say so rather than fail drawing
    const isRead = (w: Worker) =>
      Array.isArray(w?.prs) &&
      Array.isArray(w?.lastLines) &&
      Array.isArray(w?.notes) &&
      Array.isArray(w?.plan) &&
      typeof w?.status === 'string' &&
      (w.ask == null || Array.isArray(w.ask.questions))
    if (!Array.isArray(workers) || !workers.every(isRead)) {
      return { workers: [], error: `${DEV_BIN} desktop board printed workers this mod does not read: is dev up to date?`, warnings: '' }
    }
    return { workers: workers as Worker[], error: '', warnings: stderr }
  } catch (err) {
    return { workers: [], error: `${DEV_BIN} desktop board: ${errorText(err)}`, warnings: '' }
  }
}

/** A PR's CI checks from `gh pr checks`, or why it printed none (no checks yet, no gh). */
async function prChecks(run: Run, url: string): Promise<Omit<Checks, 'at'>> {
  try {
    // Exits 1 on a failed check and 8 on a pending one, printing them all the same
    const ran = await run(['gh', 'pr', 'checks', url, '--json', 'name,bucket'], { timeoutMs: 30_000 })
    const out = ran.stdout.trim()
    if (!out.startsWith('[')) {
      return { checks: [], error: ran.stderr.trim() || `gh pr checks exited ${ran.exitCode}` }
    }
    const list: { name: unknown; bucket: unknown }[] = JSON.parse(out)
    return { checks: list.map(c => ({ name: String(c.name), bucket: String(c.bucket) })), error: '' }
  } catch (err) {
    return { checks: [], error: `gh pr checks: ${errorText(err)}` }
  }
}

const label = (w: Worker) => w.name || w.title

/** Sends the boss a prompt, saying in a toast whether it was queued; whether it was. */
async function send($: EngineInterface, text: string, what: string): Promise<boolean> {
  try {
    const r = await $.prompt.submit({ text })
    await $.ui.toast(r.drop === undefined ? `dev-board: ${what} queued for the boss` : `dev-board: ${what} not sent: ${r.drop}`)
    return r.drop === undefined
  } catch (err) {
    await $.ui.toast(`dev-board: ${what} not sent: ${errorText(err)}`)
    return false
  }
}

/** What a button asks the boss, e.g. `dev-board: nudge fix-login (local_1): Continue where you left off`. */
const ask = (verb: 'message' | 'nudge' | 'archive', w: Worker, text: string) => `dev-board: ${verb} ${label(w)} (${w.id}): ${text}`

/** The answers `dev desktop answer` takes: question text to its picks, a multi-select's comma-joined as the dialog joins them. */
const answersOf = (a: Ask, p: Picks) => Object.fromEntries(a.questions.map(q => [q.question, (p.picks[q.question] ?? []).join(', ')]))

/** Whether the question is answered by one press: a single question, one pick. */
const isOnePress = (a: Ask) => a.questions.length === 1 && a.questions[0]?.multiSelect === false

/** Whether a worker's picks are for the question it waits on now. */
const picksFor = (a: Ask, p: Picks | undefined) => (p?.toolUseId === a.toolUseId ? p : undefined)

/** Whether `dev delivered` passes for a worktree, else why not (its error, or why it could not run). */
async function deliveredCheck(run: Run, worktree: string): Promise<Omit<Delivered, 'at'>> {
  try {
    const ran = await run([DEV_BIN, 'delivered', worktree], { timeoutMs: 60_000 })
    const why = ran.exitCode === 0 ? '' : (ran.stderr || ran.stdout).trim() || `${DEV_BIN} delivered exited ${ran.exitCode}`
    return { isDelivered: ran.exitCode === 0, why }
  } catch (err) {
    return { isDelivered: false, why: `${DEV_BIN} delivered: ${errorText(err)}` }
  }
}

/** The notes not sent yet, as one prompt (e.g. `dev-board: fix-login stuck: no progress for 17m; …`) and their keys. */
function unsent(workers: Worker[], sentKeys: readonly string[]): { text: string; keys: string[] } {
  const done = new Set(sentKeys)
  const lines: string[] = []
  const keys: string[] = []
  for (const w of workers) {
    for (const n of w.notes.filter(n => !done.has(n.key))) {
      lines.push(`${label(w)} ${n.kind}: ${n.text}`)
      keys.push(n.key)
    }
  }
  return { text: lines.length === 0 ? '' : `dev-board: ${lines.join('; ')}`, keys }
}

/** The band's line, e.g. `1 needs you · 3 workers · 1 busy · 1 blocked · PR #78 open`. */
function bandLine(workers: Worker[]): string {
  const open = workers.flatMap(w => w.prs.filter(pr => pr.state === 'OPEN').map(pr => `#${pr.number}`))
  const needsYou = workers.filter(w => w.status === NEEDS_YOU).length
  const parts = needsYou > 0 ? [`${needsYou} needs you`] : []
  parts.push(`${workers.length} worker${workers.length === 1 ? '' : 's'}`)
  for (const state of ['busy', 'blocked'] as const) {
    const count = workers.filter(w => w.state === state).length
    if (count > 0) {
      parts.push(`${count} ${state}`)
    }
  }
  if (open.length > 0) {
    parts.push(`${open.length === 1 ? 'PR' : 'PRs'} ${open.join(' ')} open`)
  }
  return parts.join(' · ')
}

/** Whether the tile shows what a worker not running needs (a blocked one's status says it). */
const showsNeeds = (w: Worker) => w.needsAction !== '' && w.state !== 'busy' && w.status !== 'blocked'

/** Whether the tile shows the person something to act on: a health note, a need or a question. */
const asksForPerson = (w: Worker) => w.notes.length > 0 || showsNeeds(w) || w.ask != null

/** The status and what it says, e.g. `blocked · pick one`; the status left out where its section says it (`allow Bash: rm -rf bin` under Needs you). */
const statusLine = (w: Worker, group: (typeof GROUPS)[number]) =>
  [w.status === group.toLowerCase() ? '' : w.status, w.statusText].filter(s => s !== '').join(' · ')

/** A code fence line (```ts), which says nothing on its own. */
const isFence = (line: string) => /^\s*```/.test(line)

/** A reply line without its inline markdown: a leading `- ` or `* `, backticks, and `**` or `__` around words outside code. */
const plainLine = (line: string) =>
  line
    .replace(/^\s*[-*]\s+/, '')
    .split('`')
    .map((part, i) => (i % 2 === 1 ? part : part.replace(/(?<!\w)(\*\*|__)(?=\S)(.+?)(?<=\S)\1(?!\w)/g, '$2')))
    .join('')

/** Whether a reply line says what the status does (`Blocked: pick one` beside `blocked · pick one`): their first 40 characters alike, case, inline markdown, end punctuation and a leading `<status>:` aside. */
function repeatsStatus(line: string, w: Worker): boolean {
  const said = (s: string) => plainLine(s).trim().toLowerCase().replace(/[.!…]+$/, '')
  const text = said(line)
  const rest = text.startsWith(`${w.status}:`) ? text.slice(w.status.length + 1).trimStart() : text
  const what = said(w.statusText)
  return what !== '' && rest.slice(0, 40) === what.slice(0, 40)
}

/** The verdict on its last turn and its detail; where the status says the verdict (`review_ready`), only a detail the status row lacks. */
function verdictLine(w: Worker): string {
  if (w.verdict === '') return ''
  if (w.verdict.replaceAll('_', ' ') === w.status) return w.detail === w.statusText ? '' : w.detail
  if (w.detail === '') return w.verdict
  return `${w.verdict}: ${w.detail}`
}

/** A PR's CI line, e.g. `✅ build ❌ test`, or why it has none; '' for a PR not open or not read yet. */
function ciLine(pr: PR, c: Checks | undefined): string {
  if (pr.state !== 'OPEN' || c === undefined) return ''
  if (c.error !== '') return c.error
  return c.checks.map(k => `${CHECK_ICONS[k.bucket] ?? k.bucket} ${k.name}`).join(' ')
}

/** What an expanded list shows past MAX_STEPS: the done steps before the last one folded into a count, then the steps past the room (the current one aside) into `n more`. */
function shownSteps(plan: Step[]): { earlier: number; steps: Step[]; more: number } {
  if (plan.length <= MAX_STEPS) return { earlier: 0, steps: plan, more: 0 }
  const open = plan.findIndex(s => s.status !== 'completed')
  const earlier = Math.max(0, (open === -1 ? plan.length : open) - 1)
  const rest = plan.slice(earlier)
  const room = MAX_STEPS - (earlier > 0 ? 1 : 0)
  if (rest.length <= room) return { earlier, steps: rest, more: 0 }
  // A current step past the room takes the last row, so it always shows
  const cut = rest.slice(0, room - 1)
  const current = rest.slice(room - 1).find(s => s.status === 'in_progress')
  const steps = current ? [...cut.slice(0, -1), current] : cut
  return { earlier, steps, more: rest.length - steps.length }
}

export const register: Register = on => {
  registerAsk(on)
  registerReply(on)
  // What each open message field holds, by surface and worker id (each surface's field holds its own), for its Send button
  const drafts = new Map<string, string>()
  // The workers whose answer is being written: a second press waits for it
  const answering = new Set<string>()
  on('session.start', async ($, e, next) => {
    await $.command.register({ name: 'board', description: "Show this boss session's workers in a pane" })
    const boss = await $.env.get('CLAUDE_CODE_HOST_SESSION_ID')
    if (!boss) {
      const error = 'Not a desktop app session: CLAUDE_CODE_HOST_SESSION_ID is unset.'
      await update($, board, () => ({ workers: [], error, warnings: '' }))
      return next(e)
    }
    await listenForReplies($, boss)
    const run: Run = (argv, init) => $.process.run(argv, init)
    let isPolling = false
    let last = ''
    // The PR URLs and worker ids being read: a slow gh or fetch must not stack another
    const reading = new Set<string>()
    /**
     * Reads what is due (not read for REFRESH_MS) of the open PRs' checks and the delivery of the workers with a PR
     * not busy. A busy worker's delivery is dropped: what it does may undo it. Entries gone from the board go too.
     */
    const refresh = async (workers: Worker[]) => {
      const now = await $.clock.now()
      const due = (at: number | undefined, key: string) => !reading.has(key) && (at === undefined || now - at >= REFRESH_MS)
      const openPRs = workers.flatMap(w => w.prs.filter(pr => pr.state === 'OPEN'))
      const candidates = workers.filter(w => w.prs.length > 0 && w.state !== 'busy' && w.worktree !== '')
      const keep = <T,>(m: Record<string, T>, keys: Set<string>) =>
        Object.keys(m).every(k => keys.has(k)) ? m : Object.fromEntries(Object.entries(m).filter(([k]) => keys.has(k)))
      const readChecks = await read($, checks)
      const openURLs = new Set(openPRs.map(pr => pr.url))
      if (keep(readChecks, openURLs) !== readChecks) {
        await update($, checks, m => keep(m, openURLs))
      }
      const readDelivered = await read($, delivered)
      const candidateIds = new Set(candidates.map(w => w.id))
      if (keep(readDelivered, candidateIds) !== readDelivered) {
        await update($, delivered, m => keep(m, candidateIds))
      }
      for (const pr of openPRs) {
        if (due(readChecks[pr.url]?.at, pr.url)) {
          reading.add(pr.url)
          void prChecks(run, pr.url)
            .then(got => update($, checks, m => ({ ...m, [pr.url]: { ...got, at: now } })))
            .finally(() => reading.delete(pr.url))
        }
      }
      for (const w of candidates) {
        if (due(readDelivered[w.id]?.at, w.id)) {
          reading.add(w.id)
          void deliveredCheck(run, w.worktree)
            .then(got => update($, delivered, m => ({ ...m, [w.id]: { ...got, at: now } })))
            .finally(() => reading.delete(w.id))
        }
      }
    }
    const tick = async () => {
      // A slow run must not stack another on top
      if (isPolling) return
      isPolling = true
      try {
        const polled = await poll(run, boss)
        const json = JSON.stringify(polled)
        if (json !== last) {
          await update($, board, () => polled)
          last = json
          // Picks for a question no longer asked go; a failed poll, listing no workers, says nothing of them
          const asked = new Map(polled.workers.map(w => [w.id, w.ask?.toolUseId]))
          const held = await read($, picks)
          if (polled.error === '' && Object.entries(held).some(([id, p]) => asked.get(id) !== p.toolUseId)) {
            await update($, picks, m => Object.fromEntries(Object.entries(m).filter(([id, p]) => asked.get(id) === p.toolUseId)))
          }
        }
        void refresh(polled.workers).catch((err: unknown) => $.ui.toast(`dev-board: PR checks not read: ${errorText(err)}`))
        const notes = unsent(polled.workers, await read($, sent))
        if (notes.text !== '') {
          // Marked first: the prompt waits for the boss to go idle, and a refused one is not retried every tick.
          // The notes still holding go last, so the cap drops only gone ones
          const holding = new Set(polled.workers.flatMap(w => w.notes.map(n => n.key)))
          await update($, sent, keys => [...keys.filter(k => !holding.has(k)), ...holding].slice(-MAX_SENT))
          void $.prompt.submit({ text: notes.text }).then(
            r => r.drop !== undefined && $.ui.toast(`dev-board: worker notes not sent: ${r.drop}`),
            (err: unknown) => $.ui.toast(`dev-board: worker notes not sent: ${errorText(err)}`),
          )
        }
      } finally {
        isPolling = false
      }
    }
    void tick()
    $.clock.every(PERIOD_MS, tick)

    return next(e)
  })

  // Whether a boss prompt waits on its timer; a reload cancels both
  let isAskingBoss = false
  on('command.run', { command: 'board' }, async $ => {
    const opened = await $.ui.open({ id: PANE, title: 'Workers' })
    // Sent from a timer: one sent from this hook would wait on the turn the command holds, and the host refuses it.
    // Marked as the timer fires, so a timer that never fires leaves it to the next /board; a refused prompt is not retried
    if (!isAskingBoss && (await $.env.get('CLAUDE_CODE_HOST_SESSION_ID')) && !(await read($, bossAsked))) {
      isAskingBoss = true
      $.clock.after(0, async () => {
        await update($, bossAsked, () => true)
        await send($, BOSS_PROMPT, 'boss mode')
      })
    }

    return { text: opened.isPlaced ? 'Worker board opened.' : 'Worker board opened; this surface does not place panes.' }
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const ui = $.ui.resolve(e)
    const { Box, Text, Link, Button } = ui
    // The mobile app draws no field
    const Input = e.surface !== 'mobile' && 'Input' in ui ? ui.Input : undefined
    const b = await read($, board)
    if (b === null) {
      return <Text dimColor>Loading workers…</Text>
    }
    const v = await read($, view)
    const checksRead = await read($, checks)
    const deliveredRead = await read($, delivered)
    const picksRead = await read($, picks)

    const toggle = (id: string) =>
      update($, view, s => ({ ...s, expanded: s.expanded.includes(id) ? s.expanded.filter(x => x !== id) : [...s.expanded, id] }))
    // Folding drops the tile's message field rather than keep it hidden
    const fold = (id: string, isOpen: boolean) =>
      update($, view, s => ({ ...s, tiles: { ...s.tiles, [id]: isOpen }, composing: !isOpen && s.composing === id ? '' : s.composing }))
    const forgetDrafts = (id: string) => {
      for (const key of drafts.keys()) if (key.endsWith(`:${id}`)) drafts.delete(key)
    }
    const compose = (id: string) => {
      forgetDrafts(id)
      return update($, view, s => ({ ...s, composing: s.composing === id ? '' : id }))
    }
    // Typed before a reload of the mod, a field's text never reached it: kept open for Enter rather than dropped
    const sendDraft = async (w: Worker, surface: typeof e.surface) => {
      // A message given back after a refusal is the field's text until the person types
      const text = drafts.get(`${surface}:${w.id}`) ?? (await read($, view)).unsent[w.id]
      if (text === undefined) {
        await $.ui.toast(`dev-board: no text read for ${label(w)}: press Enter in the field to send it`)
        return
      }
      await message(w, text)
    }
    /** Closes the field and sends its text; a message not sent opens it again, holding the text (kept for its next opening while another field is open). */
    const message = async (w: Worker, text: string) => {
      forgetDrafts(w.id)
      await update($, view, s => ({ ...s, composing: '', unsent: Object.fromEntries(Object.entries(s.unsent).filter(([id]) => id !== w.id)) }))
      if (text.trim() === '' || (await send($, ask('message', w, text.trim()), `message for ${label(w)}`))) return
      await update($, view, s => ({ ...s, composing: s.composing === '' ? w.id : s.composing, unsent: { ...s.unsent, [w.id]: text } }))
    }
    const copy = async (id: string, surface: typeof e.surface) => {
      try {
        // A remote surface (the desktop app) has no clipboard path yet: the Mac's then
        const copied = (await $.ui.copy({ text: id, surface })).isCopied || (await $.process.run(['pbcopy'], { stdin: id })).exitCode === 0
        await $.ui.toast(copied ? `Copied ${id}` : `dev-board: could not copy ${id}`)
      } catch (err) {
        await $.ui.toast(`dev-board: could not copy ${id}: ${errorText(err)}`)
      }
    }
    const openSession = async (w: Worker) => {
      try {
        // The app's deep link to a session; it ignores it while deep links are off (policy, logged out)
        const ran = await $.process.run(['open', `claude://code/continue?session=${w.id}`])
        if (ran.exitCode !== 0) {
          await $.ui.toast(`dev-board: could not open ${label(w)}: ${ran.stderr.trim() || `open exited ${ran.exitCode}`}`)
        }
      } catch (err) {
        await $.ui.toast(`dev-board: could not open ${label(w)}: ${errorText(err)}`)
      }
    }
    /** Picks an option: a one-press question sends it; else it toggles (one per single-select question). */
    const pick = (w: Worker, a: Ask, question: string, option: string, isMulti: boolean) => {
      const picked = (held: Picks | undefined): Picks => {
        const prior = picksFor(a, held)?.picks
        const chosen = prior?.[question] ?? []
        const toggled = chosen.includes(option) ? chosen.filter(o => o !== option) : [...chosen, option]
        return { toolUseId: a.toolUseId, picks: { ...prior, [question]: isMulti ? toggled : [option] }, isSent: false }
      }
      if (isOnePress(a)) return answer(w, a, picked(undefined))
      // From the stored picks, not this drawing's: two quick presses both count
      return update($, picks, m => ({ ...m, [w.id]: picked(m[w.id]) })).catch((err: unknown) => $.ui.toast(`dev-board: pick not kept: ${errorText(err)}`))
    }
    /** Writes the person's picks for the worker's mod to answer its dialog with (`dev desktop answer`). */
    const answer = async (w: Worker, a: Ask, p: Picks) => {
      const missing = a.questions.find(q => (p.picks[q.question] ?? []).length === 0)
      if (missing !== undefined) {
        await $.ui.toast(`dev-board: pick an answer to "${missing.question}" first`)
        return
      }
      if (answering.has(w.id)) return
      const answers = answersOf(a, p)
      if (a.reply) {
        // A wrap_up ask: the worker's own mod submits the pick as the person's reply, else the boss relays it as a message
        answering.add(w.id)
        const text = Object.values(answers).join('; ')
        const isSent = await (a.listening ? replyDirect(w, a, text) : send($, ask('message', w, text), `answer for ${label(w)}`)).finally(() => answering.delete(w.id))
        if (isSent) await update($, picks, m => ({ ...m, [w.id]: { ...p, isSent: true } })).catch((err: unknown) => $.ui.toast(`dev-board: reply not marked sent: ${errorText(err)}`))
        return
      }
      let why = ''
      answering.add(w.id)
      try {
        const ran = await $.process.run([DEV_BIN, 'desktop', 'answer', w.id, a.toolUseId], { stdin: JSON.stringify(answers) })
        why = ran.exitCode === 0 ? '' : ran.stderr.trim() || `${DEV_BIN} desktop answer exited ${ran.exitCode}`
      } catch (err) {
        why = errorText(err)
      } finally {
        answering.delete(w.id)
      }
      if (why !== '') {
        await $.ui.toast(`dev-board: ${label(w)} not answered: ${why}`)
        return
      }
      await $.ui.toast(`dev-board: answered ${label(w)}: ${Object.values(answers).join('; ')}`)
      await update($, picks, m => ({ ...m, [w.id]: { ...p, isSent: true } }))
    }
    /** Hands the reply to the worker's mod; whether it was written. */
    const replyDirect = async (w: Worker, a: Ask, text: string) => {
      const why = await writeReply($, w.id, a.toolUseId, text)
      await $.ui.toast(why === '' ? `dev-board: replied to ${label(w)}: ${text}` : `dev-board: reply for ${label(w)} not sent: ${why}`)
      return why === ''
    }
    const archive = async (w: Worker) => {
      // Checked again: the last check may be a minute old
      const got = await deliveredCheck((argv, init) => $.process.run(argv, init), w.worktree)
      if (!got.isDelivered) {
        await $.ui.toast(`dev-board: ${label(w)} not archived: ${got.why}`)
        return
      }
      await send($, ask('archive', w, 'dev delivered exited 0'), `archive of ${label(w)}`)
    }

    /** The question a worker waits on: each question's header and text, its options as buttons (their descriptions dim beside them), and Send where it takes more than one press; unanswerable from here, a note that the options open its session. */
    const questionRows = (w: Worker, a: Ask, color: string) => {
      const held = picksFor(a, picksRead[w.id])
      if (held?.isSent) {
        return (
          <Text key={`${w.id}:sent`} size="footnote" color="subtle">
            sent: {Object.values(answersOf(a, held)).join('; ')}; waiting for the worker
          </Text>
        )
      }

      return (
        <Box key={`${w.id}:ask`} flexDirection="column" rowGap="xs" marginTop="xs" minWidth={0}>
          {a.questions.map((q, qi) => {
            const chosen = held?.picks[q.question] ?? []
            const mark = (isChosen: boolean) => {
              if (isOnePress(a)) return ''
              if (q.multiSelect) return isChosen ? '☑ ' : '☐ '
              return isChosen ? '◉ ' : '○ '
            }

            return (
              <Box key={`${w.id}:q${qi}`} flexDirection="column" rowGap="xs" minWidth={0}>
                <Box flexDirection="column" minWidth={0}>
                  {q.header !== '' && (
                    <Text size="caption" color={color} weight="semibold">
                      {q.header}
                    </Text>
                  )}
                  <Text weight="semibold">{q.question}</Text>
                </Box>
                {q.options.map((o, oi) => {
                  const isChosen = chosen.includes(o.label)
                  // Without the worker's mod watching, only its own dialog answers: the option opens it
                  const press = () => void (a.answerable || a.reply ? pick(w, a, q.question, o.label, q.multiSelect) : openSession(w))

                  return (
                    <Box key={`${w.id}:q${qi}:o${oi}`} flexDirection="row" alignItems="flex-start" columnGap={1} minWidth={0}>
                      <Box flexShrink={0}>
                        <Button key={`${w.id}:q${qi}:o${oi}:pick`} label={`${mark(isChosen)}${o.label}`} variant={isChosen ? 'primary' : undefined} onPress={press} />
                      </Box>
                      {o.description !== '' && (
                        <Box flexShrink={1} minWidth={0}>
                          <Text size="footnote" dimColor>
                            {o.description}
                          </Text>
                        </Box>
                      )}
                    </Box>
                  )
                })}
              </Box>
            )
          })}
          {!a.answerable && !a.reply && (
            <Text size="footnote" color="subtle">
              answered in its session: an option opens it
            </Text>
          )}
          {a.reply && a.questions.every(q => q.options.length === 0) && Input !== undefined && (
            <Text size="footnote" color="subtle">
              answer it with Message…
            </Text>
          )}
          {a.answerable && !isOnePress(a) && (
            <Box flexDirection="row">
              <Button key={`${w.id}:answer`} label="Send" variant="primary" onPress={() => void answer(w, a, held ?? { toolUseId: a.toolUseId, picks: {}, isSent: false })} />
            </Box>
          )}
        </Box>
      )
    }

    /** A step's row: its mark and its text in cells of their own, the text cut to one line; a count of steps looks as they do. */
    const stepRow = (key: string, status: Step['status'], text: string, color: string) => {
      const look = STEP_LOOKS[status] ?? STEP_LOOKS.pending
      return (
        <Box key={key} flexDirection="row" columnGap={1} minWidth={0}>
          <Box flexShrink={0}>
            <Text size="footnote" color={look.markColor ?? color} dimColor={look.isDim}>
              {look.mark}
            </Text>
          </Box>
          <Box flexShrink={1} minWidth={0}>
            <Text size="footnote" color={look.color} weight={look.weight} dimColor={look.isDim} wrap="truncate-end">
              {text}
            </Text>
          </Box>
        </Box>
      )
    }

    /** The task list: its toggle, a progress bar and count; collapsed, only the step at hand under it; expanded, the steps. */
    const taskList = (w: Worker, color: string, done: number, isExpanded: boolean) => {
      const atHand = w.plan.find(s => s.status === 'in_progress') ?? w.plan.find(s => s.status === 'pending')
      const cells = Math.round((done / w.plan.length) * BAR_CELLS)
      const { earlier, steps, more } = isExpanded ? shownSteps(w.plan) : { earlier: 0, steps: atHand ? [atHand] : [], more: 0 }
      return (
        <Box key={`${w.id}:plan`} flexDirection="column" marginTop="xs" minWidth={0}>
          <Box flexDirection="row" alignItems="center" columnGap={1}>
            <Button key={`${w.id}:tasks`} label={isExpanded ? 'Tasks ▾' : 'Tasks ▸'} plain dimColor onPress={() => void toggle(w.id)} />
            {e.surface === 'terminal' ? (
              // Whole cells: a fifth of a row would draw nothing
              <Box key={`${w.id}:bar`}>
                <Text>
                  <Text color="success">{'━'.repeat(cells)}</Text>
                  <Text color="inactive">{'━'.repeat(BAR_CELLS - cells)}</Text>
                </Text>
              </Box>
            ) : (
              <Box key={`${w.id}:bar`} flexDirection="row" alignItems="center" width={BAR_CELLS} height={1}>
                <Box flexDirection="row" flexGrow={1} height="20%" backgroundColor="inactive">
                  <Box flexGrow={done} backgroundColor="success" />
                  <Box flexGrow={w.plan.length - done} />
                </Box>
              </Box>
            )}
            <Text size="footnote" color="subtle" tabular>
              {`${done}/${w.plan.length}`}
            </Text>
          </Box>
          {earlier > 0 && stepRow(`${w.id}:earlier`, 'completed', `${earlier} done`, color)}
          {steps.map(s => stepRow(`${w.id}:step${w.plan.indexOf(s)}`, s.status, s.content, color))}
          {more > 0 && stepRow(`${w.id}:more`, 'pending', `${more} more`, color)}
        </Box>
      )
    }

    /** One worker's tile: folded, one row; open, the fold button and dot beside its rows, every one under the title. */
    const tile = (w: Worker, group: (typeof GROUPS)[number], isOpen: boolean) => {
      const color = GROUP_COLORS[group]
      const done = w.plan.filter(s => s.status === 'completed').length
      const isExpanded = v.expanded.includes(w.id)
      const delivery = w.state === 'busy' ? undefined : deliveredRead[w.id]
      const meta = [w.branch, w.context === null ? '' : `context ${w.context}%`].filter(s => s !== '').join(' · ')
      const verdict = verdictLine(w)
      const foldButton = <Button key={`${w.id}:fold`} label={isOpen ? '▾' : '▸'} plain dimColor onPress={() => void fold(w.id, !isOpen)} />
      const dot = <Text color={STATE_COLORS[w.state]}>●</Text>
      // A Text nested in a cut one wraps on the desktop (each Text sets its own white-space): what may be cut is one
      // Text of plain strings, the dot left outside it
      const name = (
        <Text weight="semibold" color={isOpen ? undefined : 'subtle'} wrap="truncate-end">
          {w.title || w.name}
        </Text>
      )
      const progress = w.plan.length > 0 && (
        <Text size="footnote" color="subtle" tabular>
          {`${done}/${w.plan.length}`}
        </Text>
      )
      const statusText = plainLine(statusLine(w, group))
      const status = statusText !== '' && (
        <Text size="footnote" color={color} weight={group === 'Needs you' ? 'semibold' : 'regular'} wrap="truncate-end">
          {isOpen ? statusText : `· ${statusText}`}
        </Text>
      )
      const sinceText = w.since === '0m' ? 'now' : w.since
      const since = sinceText !== '' && (
        <Text size="footnote" color="subtle" tabular>
          {isOpen ? sinceText : `· ${sinceText}`}
        </Text>
      )
      if (!isOpen) {
        // Lined up with an open tile's title, past its rule; the title and status are cut, the rest kept
        return (
          <Box key={`${w.id}:header`} flexDirection="row" alignItems="flex-start" columnGap={1} paddingLeft={2} minWidth={0}>
            {foldButton}
            {dot}
            <Box flexShrink={1} minWidth={0}>
              {name}
            </Box>
            {progress && <Box flexShrink={0}>{progress}</Box>}
            {status && (
              <Box flexShrink={1} minWidth={0}>
                {status}
              </Box>
            )}
            {since && <Box flexShrink={0}>{since}</Box>}
            {/* Outside the cut text, so truncation never hides it */}
            {asksForPerson(w) && (
              <Text size="footnote" color="warning">
                ⚠
              </Text>
            )}
          </Box>
        )
      }

      return (
        <Box flexDirection="row" alignItems="flex-start" columnGap={1} borderStyle="quote" borderColor={color} paddingLeft={1} minWidth={0}>
          {foldButton}
          {dot}
          <Box key={`${w.id}:rows`} flexDirection="column" flexGrow={1} flexShrink={1} minWidth={0}>
            {/* Never wraps: the title is cut, its time kept at the right */}
            <Box key={`${w.id}:header`} flexDirection="row" columnGap={1} minWidth={0}>
              <Box flexGrow={1} flexShrink={1} minWidth={0}>
                {name}
              </Box>
              <Box flexShrink={0}>{since}</Box>
            </Box>
            {w.ask ? questionRows(w, w.ask, color) : status}
            {(meta !== '' || w.prs.length > 0) && (
              <Text size="caption">
                <Text color="subtle">{meta}</Text>
                {w.prs.map((pr, i) => {
                  const c = checksRead[pr.url]
                  const ci = ciLine(pr, c)

                  return (
                    <Text key={pr.url}>
                      <Text color="subtle">{meta !== '' || i > 0 ? ' · ' : ''}</Text>
                      <Link href={pr.url} label={`PR #${pr.number}`} /> <Text color={PR_COLORS[pr.state] ?? 'inactive'}>{pr.state.toLowerCase()}</Text>
                      {ci !== '' && <Text dimColor={c?.error !== ''}>{` ${ci}`}</Text>}
                    </Text>
                  )
                })}
              </Text>
            )}
            {verdict !== '' && (
              <Text size="footnote" dimColor={w.state === 'busy'} wrap="truncate-end">
                {w.state === 'busy' ? 'last turn: ' : ''}
                {verdict}
              </Text>
            )}
            {showsNeeds(w) && (
              <Text size="footnote" color="warning" weight="medium">
                needs: {w.needsAction}
              </Text>
            )}
            {delivery?.isDelivered === false && (
              <Text size="footnote" color="subtle">
                not delivered: {delivery.why}
              </Text>
            )}
            {w.notes.map(n => (
              <Text key={n.key} size="footnote" color="warning" weight="medium">
                {n.kind}: {n.text}
              </Text>
            ))}
            {w.lastLines
              .filter(line => !isFence(line) && !repeatsStatus(line, w))
              .slice(-MAX_LAST_LINES)
              .map(line => (
                <Text size="footnote" dimColor italic wrap="truncate-end">
                  {plainLine(line)}
                </Text>
              ))}
            {w.plan.length > 0 && taskList(w, color, done, isExpanded)}
            <Box flexDirection="row" flexWrap="wrap" columnGap={1} marginTop="xs">
              <Button
                key={`${w.id}:open`}
                label="Open"
                variant={group === 'Needs you' || group === 'Waiting' ? 'primary' : undefined}
                onPress={() => void openSession(w)}
              />
              {Input !== undefined && <Button key={`${w.id}:message`} label="Message…" onPress={() => void compose(w.id)} />}
              <Button key={`${w.id}:nudge`} label="Nudge" onPress={() => void send($, ask('nudge', w, NUDGE), `nudge for ${label(w)}`)} />
              {delivery?.isDelivered === true && <Button key={`${w.id}:archive`} label="Archive" onPress={() => void archive(w)} />}
              <Button key={`${w.id}:copy`} label="Copy id" plain onPress={press => void copy(w.id, press.surface)} />
            </Box>
            {Input !== undefined && v.composing === w.id && (
              <Box flexDirection="row" flexWrap="wrap" alignItems="center" columnGap={1} marginTop="xs" minWidth={0}>
                {/* A short placeholder: a one-line field does not grow on the desktop, so its own width must hold it */}
                <Input
                  key={`${w.id}:draft`}
                  placeholder="Message…"
                  value={v.unsent[w.id]}
                  autoFocus
                  onInput={(text, input) => {
                    drafts.set(`${input.surface}:${w.id}`, text)
                    // A given-back text follows the person's edits, so a redraw never puts the refused one back
                    if (v.unsent[w.id] !== undefined) void update($, view, s => ({ ...s, unsent: { ...s.unsent, [w.id]: text } }))
                  }}
                  onSubmit={text => void message(w, text)}
                />
                <Button key={`${w.id}:send`} label="Send" variant="primary" onPress={press => void sendDraft(w, press.surface)} />
                <Button key={`${w.id}:cancel`} label="Cancel" plain onPress={() => void message(w, '')} />
              </Box>
            )}
          </Box>
        </Box>
      )
    }
    // A status a newer dev adds shows under Working, unfolded, rather than not at all
    const sections = GROUPS.map(group => ({ group, workers: b.workers.filter(w => (Object.hasOwn(GROUP_OF, w.status) ? GROUP_OF[w.status] : 'Working') === group) }))
      .filter(s => s.workers.length > 0)

    return (
      <Box flexDirection="column" gap="md">
        {b.error !== '' && <Text color="error">{b.error}</Text>}
        {b.warnings !== '' && <Text color="warning">{b.warnings}</Text>}
        {b.error === '' && b.workers.length === 0 && <Text dimColor>No workers.</Text>}
        {sections.map(({ group, workers }) => (
          <Box key={group} flexDirection="column" rowGap="xs">
            <Box flexDirection="row" columnGap={1}>
              <Text size="footnote" weight="semibold" color={GROUP_COLORS[group]}>
                {group.toUpperCase()}
              </Text>
              <Text size="footnote" color="subtle" tabular>
                {workers.length}
              </Text>
            </Box>
            {workers.map(w => (
              <Box key={w.id} flexDirection="column" minWidth={0}>{tile(w, group, v.tiles[w.id] ?? (group !== 'Idle' || asksForPerson(w) || v.composing === w.id))}</Box>
            ))}
          </Box>
        ))}
      </Box>
    )
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const b = await read($, board)
    if (b === null || b.workers.length === 0 || e.props.hasSurvey) {
      return next(e)
    }
    const { Text } = $.ui.resolve(e)
    const line = bandLine(b.workers)

    return b.workers.some(w => w.status === NEEDS_YOU) ? <Text color="error">{line}</Text> : <Text dimColor>{line}</Text>
  })
}

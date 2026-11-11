import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { Ask, Board, Checks, Delivered, Picks, PR, Status, Step, Todo, TodoItem, Worker } from '../types'
import { registerAsk } from './ask'
import { listenForReplies, registerReply, writeReply } from './reply'

// The dev binary with `dev desktop board`: an absolute path when the app's PATH lacks it
const DEV_BIN = 'dev'

const PANE = 'board'
const PERIOD_MS = 2000
// PR checks and `dev delivered` (a git fetch, gh) are read at most this often per PR or worker
const REFRESH_MS = 60_000
const NEEDS_YOU = 'needs you'
const BOSS_PROMPT = "dev-board: /board opened — load the dev-board:boss skill and act as boss for this session's workers."
// The desktop pane's tokens: five colors (theme keys, so light and dark follow the theme), two sizes, two weights, four spacing steps;
// besides them only BAR_TRACK's grey and the icons' white. The terminal and the phones keep their own looks
const TONE = { text: 'text', muted: 'subtle', ok: 'success', caution: 'warning', danger: 'error' } as const
// A tone in an SVG icon, which can't name a theme key: each about 4.2:1 on the light (#FFFFFF) and the dark (#181818) pane
const TONE_HEX = { ok: '#278D43', caution: '#A27300', danger: '#E33C41', muted: '#7B7B7B' } as const
const SIZE = { body: 'body', meta: 'footnote' } as const
const WEIGHT = { regular: 'regular', strong: 'semibold' } as const
// Across a row (cells), between a row's lines, a notice's padding (off the desktop also the gap between blocks), between the to do list, warnings and sections
const SPACE = { inline: 1, line: 'xs', block: 'sm', section: 'md' } as const
// The pane's sections, in order; idle workers without a row of their own (isFull) share one line
const GROUPS = ['Needs you', 'Working', 'Waiting', 'Idle'] as const
const GROUP_COLORS = { 'Needs you': 'error', Working: 'suggestion', Waiting: 'warning', Idle: 'inactive' } as const
// The dot says the row's section, not the app's state, so a worker whose open dialog waits on the person is red, not busy's green; on the desktop also its header, a question's header and the current step
const DOT_COLORS = { 'Needs you': TONE.danger, Working: TONE.ok, Waiting: TONE.caution, Idle: TONE.muted } as const
// The sections whose rows ask to be looked at: only they carry their buttons on line 1
const ATTENTION_GROUPS: readonly (typeof GROUPS)[number][] = ['Needs you', 'Waiting']
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
// The words a reply line listing a question's options may hold besides them, e.g. `Pick a reply: Per package, or Total only.`
const LIST_WORDS = new Set(['pick', 'choose', 'select', 'reply', 'answer', 'one', 'of', 'a', 'an', 'the', 'or', 'and', 'either', 'with', 'please', 'which', 'do', 'you', 'want', 'prefer', 'should', 'i'])
// An option label's `(Recommended)` suffix, which the board shows as a ★ before the label
const RECOMMENDED = /\s*\(recommended\)\s*$/i

// What dev adds to a busy worker's status text for its background work, left out where a reply line is compared with it
const BACKGROUND = /(^|\s·\s)\d+ background running$/
// A reply handed to a worker's mod that it still waits for after this was not taken: the board offers the ask again
const RETAKE_MS = 10_000
// One glyph family so the marks line up: done recedes, the current step takes the section's color, pending stays quiet
const STEP_LOOKS = {
  completed: { mark: '●', markColor: 'success', color: 'inactive', weight: 'regular', isDim: true },
  in_progress: { mark: '◐', markColor: undefined, color: 'text', weight: 'medium', isDim: false },
  pending: { mark: '○', markColor: 'subtle', color: 'subtle', weight: 'regular', isDim: false },
} as const
// A detail's task list rows at most, its count rows included; the progress bar's width in cells
const MAX_STEPS = 7
const BAR_CELLS = 5
// The desktop bar's empty track and the rule between rows: a faint grey that reads as empty on light and dark alike (the theme's greys draw a full-looking bar)
const BAR_TRACK = 'rgba(128, 128, 128, 0.22)'
// A PR's CI checks summed up: any failed (or cancelled), else any still running, else passed (passed or skipped)
type Rollup = 'failed' | 'running' | 'passed'
const FAILED_BUCKETS = ['fail', 'cancel']
const PASSED_BUCKETS = ['pass', 'skipping']
// Icons are Svg (its alt read aloud) where the surface draws it, in hues that hold on light and dark; the terminal draws the glyph in the theme's color
type Icon = { svg: string; glyph: string; color: string }
const icon = (body: string, glyph: string, color: string): Icon => ({
  svg: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" width="16" height="16">${body}</svg>`,
  glyph,
  color,
})
const stroke = (hex: string) => `fill="none" stroke="${hex}" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"`
type Hues = { open: string; merged: string; closed: string; passed: string; failed: string; running: string }
const iconsIn = (hue: Hues) => ({
  pr: {
    OPEN: icon(
      `<g ${stroke(hue.open)}><circle cx="4" cy="3.5" r="1.8"/><circle cx="4" cy="12.5" r="1.8"/><circle cx="12" cy="12.5" r="1.8"/><path d="M4 5.3v5.4M12 10.7V6.5a2 2 0 0 0-2-2H7.5M9.3 2.7 7.5 4.5l1.8 1.8"/></g>`,
      '○',
      'success',
    ),
    MERGED: icon(
      `<g ${stroke(hue.merged)}><circle cx="4" cy="3.5" r="1.8"/><circle cx="4" cy="12.5" r="1.8"/><circle cx="12" cy="8.5" r="1.8"/><path d="M4 5.3v5.4M4 5.3c0 2.2 2.2 3.2 6.2 3.2"/></g>`,
      '◆',
      'merged',
    ),
    CLOSED: icon(
      `<g ${stroke(hue.closed)}><circle cx="4" cy="3.5" r="1.8"/><circle cx="4" cy="12.5" r="1.8"/><circle cx="12" cy="12.5" r="1.8"/><path d="M4 5.3v5.4M12 10.7V7.5M10.2 2.2l3.6 3.6M13.8 2.2l-3.6 3.6"/></g>`,
      '✕',
      'error',
    ),
  } as Record<string, Icon>,
  ci: {
    passed: icon(`<circle cx="8" cy="8" r="7" fill="${hue.passed}"/><path d="m4.8 8.2 2.2 2.2 4.2-4.6" fill="none" stroke="#fff" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>`, '✓', 'success'),
    failed: icon(`<circle cx="8" cy="8" r="7" fill="${hue.failed}"/><path d="m5.5 5.5 5 5m0-5-5 5" fill="none" stroke="#fff" stroke-width="1.8" stroke-linecap="round"/>`, '✗', 'error'),
    running: icon(`<circle cx="8" cy="8" r="6.2" fill="none" stroke="${hue.running}" stroke-width="1.6"/><path d="M8 4.6V8l2.4 1.6" fill="none" stroke="${hue.running}" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>`, '…', 'warning'),
  } as Record<Rollup, Icon>,
})
// GitHub's hues, about 3.1-3.2:1 on white for the green and amber
const ICONS = iconsIn({ open: '#2DA44E', merged: '#8957E5', closed: '#E5484D', passed: '#2DA44E', failed: '#E5484D', running: '#BF8700' })
// The tones' hues; a merged PR is done, so muted
const DESKTOP_ICONS = iconsIn({ open: TONE_HEX.ok, merged: TONE_HEX.muted, closed: TONE_HEX.danger, passed: TONE_HEX.ok, failed: TONE_HEX.danger, running: TONE_HEX.caution })
const ICON_PX = 12
// The to do list's items shown before `+N more`, as on the tmux board; a link's PR number, as dev reads it
const TODO_SHOWN = 5
const PR_LINK = /\/(?:pull|merge_requests)\/(\d+)(?:[/?#]|$)/

type Run = (argv: string[], init?: { timeoutMs?: number; stdin?: string }) => Promise<{ exitCode: number; stdout: string; stderr: string }>

// Change the tag when Board's, Worker's, PR's, Note's, Step's, Ask's or Question's fields change: the atom then declines what an earlier version stored
const board = atom({ plugin: 'dev-board', key: 'board' } as const, null, { shape: 'board-9' })
// The keys of the notes already sent to the boss, newest last, so a reload sends none again
const sent = atom({ plugin: 'dev-board', key: 'sent' } as const, [], { shape: 'sent-1' })
const MAX_SENT = 500
// By PR URL
const checks = atom({ plugin: 'dev-board', key: 'checks' } as const, {}, { shape: 'checks-1' })
// By worker id
const delivered = atom({ plugin: 'dev-board', key: 'delivered' } as const, {}, { shape: 'delivered-2' })
const view = atom({ plugin: 'dev-board', key: 'view' } as const, { composing: '', details: {}, unsent: {}, idleOpen: false }, { shape: 'view-4' })
// Whether /board asked this session to be the boss, so it asks once per session
const bossAsked = atom({ plugin: 'dev-board', key: 'bossAsked' } as const, false, { shape: 'boss-asked-1' })
// The person's picks on the workers' questions, by worker id
const picks = atom({ plugin: 'dev-board', key: 'picks' } as const, {}, { shape: 'picks-2' })
// The boss's to do list, null when empty or not read
const todo = atom({ plugin: 'dev-board', key: 'todo' } as const, null, { shape: 'todo-1' })

const errorText = (err: unknown) => (err instanceof Error ? err.message : String(err))

const NOT_DESKTOP = 'Not a desktop app session: CLAUDE_CODE_HOST_SESSION_ID is unset.'
// What `dev desktop board` starts each stderr line with, which the pane's ⚠ says
const WARNING_PREFIX = /^⚠\s*(dev desktop board:\s*)?/

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

/** How long ago, as dev says it: `now` under a minute, `12m`, `2h`, `1h 5m`. */
function span(ms: number): string {
  const m = Math.floor(ms / 60_000)
  if (m < 1) return 'now'
  if (m < 60) return `${m}m`
  return m % 60 === 0 ? `${m / 60}h` : `${Math.floor(m / 60)}h ${m % 60}m`
}

/** The boss's to do list from `dev todo --json`, null when empty; throws when it can't be read (a dev without `dev todo`, a worker's session). */
async function readTodo(run: Run, boss: string, now: number): Promise<Todo | null> {
  const ran = await run([DEV_BIN, 'todo', '--json', '--boss', boss], { timeoutMs: 10_000 })
  if (ran.exitCode !== 0) throw new Error(ran.stderr.trim() || `${DEV_BIN} todo exited ${ran.exitCode}`)
  const list: { items?: TodoItem[]; updated?: number; stale?: boolean } = JSON.parse(ran.stdout)
  const isOptional = (v: unknown) => v === undefined || typeof v === 'string'
  const isRead = (it: TodoItem) => typeof it?.id === 'number' && typeof it.text === 'string' && typeof it.done === 'boolean' && isOptional(it.on) && isOptional(it.link)
  if (!Array.isArray(list?.items) || !list.items.every(isRead) || typeof list.updated !== 'number') {
    throw new Error(`${DEV_BIN} todo printed a list this mod does not read`)
  }
  return list.items.length === 0 ? null : { items: list.items, age: span(now - list.updated * 1000), stale: list.stale === true }
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
    await $.ui.toast(r.drop === undefined ? `${what} queued for the boss` : `${what} not sent: ${r.drop}`)
    return r.drop === undefined
  } catch (err) {
    await $.ui.toast(`${what} not sent: ${errorText(err)}`)
    return false
  }
}

/** What a button asks the boss, e.g. `dev-board: message fix-login (local_1): rebase on main`. */
const ask = (verb: 'message' | 'archive', w: Worker, text: string) => `dev-board: ${verb} ${label(w)} (${w.id}): ${text}`

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

/** The band's line, e.g. `1 needs you · 3 workers · 1 busy · 1 blocked · PR #78 open`; a worker that needs you is counted there only. */
function bandLine(workers: Worker[]): string {
  const open = workers.flatMap(w => w.prs.filter(pr => pr.state === 'OPEN').map(pr => `#${pr.number}`))
  const needsYou = workers.filter(w => w.status === NEEDS_YOU).length
  const parts = needsYou > 0 ? [`${needsYou} needs you`] : []
  parts.push(`${workers.length} worker${workers.length === 1 ? '' : 's'}`)
  for (const state of ['busy', 'blocked'] as const) {
    const count = workers.filter(w => w.state === state && w.status !== NEEDS_YOU).length
    if (count > 0) {
      parts.push(`${count} ${state}`)
    }
  }
  if (open.length > 0) {
    parts.push(`${open.length === 1 ? 'PR' : 'PRs'} ${open.join(' ')} open`)
  }
  return parts.join(' · ')
}

/** The pane's title, e.g. `Workers · 3 · 1 needs you`. */
function paneTitle(workers: Worker[]): string {
  const needsYou = workers.filter(w => w.status === NEEDS_YOU).length
  const parts = ['Workers']
  if (workers.length > 0) parts.push(`${workers.length}`)
  if (needsYou > 0) parts.push(`${needsYou} needs you`)
  return parts.join(' · ')
}

/** Whether the worker's question has options, its buttons until a reply is sent. */
const hasOptions = (w: Worker) => w.ask?.questions.some(q => q.options.length > 0) === true

/** Whether a worker may be archived: not running a turn, nor waiting on something that will wake it (its own `WAITING (…)` marker). */
const canArchive = (w: Worker) => w.state !== 'busy' && w.status !== 'waiting'

/** Whether the row shows what a worker not running needs (a blocked one's status says it, a question's options ask it). */
const showsNeeds = (w: Worker) => w.needsAction !== '' && w.state !== 'busy' && w.status !== 'blocked' && !hasOptions(w)

/** Whether the row shows the person something to act on: a health note, a need or a question. */
const asksForPerson = (w: Worker) => w.notes.length > 0 || showsNeeds(w) || w.ask != null

/**
 * The status and what it says, e.g. `blocked · pick one`; the status left out where its section says it (`allow Bash: rm -rf bin`
 * under Needs you), save a working worker's between tools, so line 1 keeps a status as the tool comes and goes.
 */
const statusLine = (w: Worker, group: (typeof GROUPS)[number]) =>
  w.status === 'working' && w.statusText === '' ? 'working' : [w.status === group.toLowerCase() ? '' : w.status, w.statusText].filter(s => s !== '').join(' · ')

/** A code fence line (```ts), which says nothing on its own. */
const isFence = (line: string) => /^\s*```/.test(line)

/** A turn-end marker line (`IDLE`, `WAITING (CI)`), as dev reads it for the status. */
const isMarker = (line: string) => /^(IDLE|WAITING \(.*)$/.test(line.replace(/^[*_` ]+|[*_` ]+$/g, ''))

/** A reply line without its inline markdown: a leading `- ` or `* `, backticks, and outside code `**` or `__` around words and a link's `[text](url)` for its text. */
const plainLine = (line: string) =>
  line
    .replace(/^\s*[-*]\s+/, '')
    .split('`')
    .map((part, i) => (i % 2 === 1 ? part : part.replace(/!?\[([^\]]+)\]\((?:[^()]|\([^()]*\))*\)/g, '$1').replace(/(?<!\w)(\*\*|__)(?=\S)(.+?)(?<=\S)\1(?!\w)/g, '$2')))
    .join('')

/** A pattern matching text where no letter or digit adjoins it. */
const wholeWord = (text: string) => new RegExp(`(?<![\\p{L}\\p{N}])${text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(?![\\p{L}\\p{N}])`, 'u')

/**
 * Whether a reply line says what the row does above it: the status (`Blocked: pick one` beside `blocked · pick one`), a
 * question it asks or the need its options answer, their first 40 characters alike, case, inline markdown, end punctuation
 * and a leading `<status>:` aside; or a question's options, every one named as a whole word, among list words only.
 */
function repeatsTile(line: string, w: Worker): boolean {
  const said = (s: string) => plainLine(s).trim().toLowerCase().replace(/[.!?…]+$/, '')
  const text = said(line)
  const rest = text.startsWith(`${w.status}:`) ? text.slice(w.status.length + 1).trimStart() : text
  const shown = [w.statusText.replace(BACKGROUND, ''), hasOptions(w) ? w.needsAction : '', ...(w.ask?.questions.map(q => q.question) ?? [])].map(said)
  if (shown.some(what => what !== '' && rest.slice(0, 40) === what.slice(0, 40))) return true
  return (w.ask?.questions ?? []).some(q => {
    const labels = q.options.map(o => said(o.label)).filter(l => l !== '')
    if (labels.length === 0) return false
    let left = rest
    for (const l of labels) {
      const named = wholeWord(l)
      if (!named.test(left)) return false
      left = left.replace(named, '')
    }
    // What is left only leads in and joins them: `A is safe; B deletes data` says more
    return left.split(/[^\p{L}\p{N}]+/u).every(word => word === '' || LIST_WORDS.has(word))
  })
}

/** An open PR's checks summed up; none while not read, unreadable, or with no checks. */
function ciRollup(pr: PR, c: Checks | undefined): Rollup | undefined {
  if (pr.state !== 'OPEN' || c === undefined || c.error !== '' || c.checks.length === 0) return undefined
  if (c.checks.some(k => FAILED_BUCKETS.includes(k.bucket))) return 'failed'
  if (c.checks.some(k => !PASSED_BUCKETS.includes(k.bucket))) return 'running'
  return 'passed'
}

/** The names of the checks that make the rollup (the failed ones, the running ones, else all), e.g. `test lint`. */
function ciNames(c: Checks, rollup: Rollup): string {
  const makes = (bucket: string) => {
    if (rollup === 'failed') return FAILED_BUCKETS.includes(bucket)
    if (rollup === 'running') return !PASSED_BUCKETS.includes(bucket) && !FAILED_BUCKETS.includes(bucket)
    return true
  }
  return c.checks.filter(k => makes(k.bucket)).map(k => k.name).join(' ')
}

/** What the detail's task list shows past MAX_STEPS: the done steps before the last one folded into a count, then the steps past the room (the current one aside) into `n more`. */
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
  // The title last given to the pane, or found on it; /board forgets it, as a poll may land between its read and its open
  let titled: string | undefined
  on('session.start', async ($, e, next) => {
    await $.command.register({ name: 'board', description: "Show this boss session's workers in a pane" })
    const boss = await $.env.get('CLAUDE_CODE_HOST_SESSION_ID')
    if (!boss) {
      await update($, board, () => ({ workers: [], error: NOT_DESKTOP, warnings: '' }))
      return next(e)
    }
    await listenForReplies($, boss)
    const run: Run = (argv, init) => $.process.run(argv, init)
    let isPolling = false
    let last = ''
    let isRetitling = false
    let isReadingTodo = false
    let lastTodo = ''
    // Each cause said once: on a dev without `dev todo`, or in a worker's session, it fails on every tick
    let lastTodoError = ''
    const logTodo = (err: unknown) => {
      if (errorText(err) === lastTodoError) return
      lastTodoError = errorText(err)
      $.ui.log(`dev-board: no to do list: ${lastTodoError}`, { to: 'debug' })
    }
    /** Reads the to do list, off the board's path; one that can't be read shows no block. */
    const refreshTodo = async () => {
      if (isReadingTodo) return
      isReadingTodo = true
      try {
        const now = await $.clock.now()
        const got = await readTodo(run, boss, now).then(
          list => {
            lastTodoError = ''
            return list
          },
          (err: unknown) => {
            logTodo(err)
            return null
          },
        )
        const json = JSON.stringify(got)
        if (json !== lastTodo) {
          await update($, todo, () => got)
          lastTodo = json
        }
      } finally {
        isReadingTodo = false
      }
    }
    /** Gives an open pane the title once per title (an open id only retitles); a failure is said once. */
    const retitle = async (title: string) => {
      titled = title
      try {
        const pane = (await $.ui.panes()).find(p => p.id === PANE)
        if (pane !== undefined && pane.title !== title) await $.ui.open({ id: PANE, title })
      } catch (err) {
        await $.ui.toast(`pane not retitled: ${errorText(err)}`)
      }
    }
    // The PR URLs and worker ids being read: a slow gh or fetch must not stack another
    const reading = new Set<string>()
    /**
     * Reads what is due (not read for REFRESH_MS) of the open PRs' checks and the delivery of the workers with a PR
     * that may be archived. The delivery of a busy or waiting worker is dropped: what it does may undo it. Entries gone from the board go too.
     */
    const refresh = async (workers: Worker[]) => {
      const now = await $.clock.now()
      const due = (at: number | undefined, key: string) => !reading.has(key) && (at === undefined || now - at >= REFRESH_MS)
      const openPRs = workers.flatMap(w => w.prs.filter(pr => pr.state === 'OPEN'))
      const candidates = workers.filter(w => w.prs.length > 0 && canArchive(w) && w.worktree !== '')
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
      void refreshTodo().catch(logTodo)
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
        const now = await $.clock.now()
        const untaken = Object.entries(await read($, picks)).filter(([id, p]) => {
          const a = polled.workers.find(w => w.id === id)?.ask
          return p.sentAt !== undefined && now - p.sentAt >= RETAKE_MS && a?.listening === true && a.toolUseId === p.toolUseId
        })
        if (untaken.length > 0) {
          const ids = new Set(untaken.map(([id]) => id))
          await update($, picks, m => Object.fromEntries(Object.entries(m).map(([id, p]) => [id, ids.has(id) ? { ...p, isSent: false, sentAt: undefined } : p])))
          for (const w of polled.workers.filter(w => ids.has(w.id))) await $.ui.toast(`${label(w)} did not take the reply: press it again`)
        }
        // A failed poll keeps the last title; off the poll's path, one at a time
        const title = paneTitle(polled.workers)
        if (polled.error === '' && title !== titled && !isRetitling) {
          isRetitling = true
          void retitle(title).finally(() => (isRetitling = false))
        }
        void refresh(polled.workers).catch((err: unknown) => $.ui.toast(`PR checks not read: ${errorText(err)}`))
        const notes = unsent(polled.workers, await read($, sent))
        if (notes.text !== '') {
          // Marked first: the prompt waits for the boss to go idle, and a refused one is not retried every tick.
          // The notes still holding go last, so the cap drops only gone ones
          const holding = new Set(polled.workers.flatMap(w => w.notes.map(n => n.key)))
          await update($, sent, keys => [...keys.filter(k => !holding.has(k)), ...holding].slice(-MAX_SENT))
          void $.prompt.submit({ text: notes.text }).then(
            r => r.drop !== undefined && $.ui.toast(`worker notes not sent: ${r.drop}`),
            (err: unknown) => $.ui.toast(`worker notes not sent: ${errorText(err)}`),
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
    const opened = await $.ui.open({ id: PANE, title: paneTitle((await read($, board))?.workers ?? []) })
    titled = undefined
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
    const Svg = e.surface !== 'terminal' && 'Svg' in ui ? ui.Svg : undefined
    // The desktop draws from the tokens only: a dim text is the muted tone there, a caption the meta size, a medium weight the strong one
    const isDesktop = e.surface === 'desktop'
    const { pr: PR_ICONS, ci: CI_ICONS } = isDesktop ? DESKTOP_ICONS : ICONS
    const faint = isDesktop ? { color: TONE.muted } : { dimColor: true }
    const small = isDesktop ? SIZE.meta : 'caption'
    const medium = isDesktop ? WEIGHT.strong : 'medium'
    const sectionColor = (group: (typeof GROUPS)[number]) => (isDesktop ? DOT_COLORS : GROUP_COLORS)[group]
    /** The pane's one message in place of rows: a glyph, its text and a dim hint, centred. */
    const notice = (key: string, text: string, glyph?: { mark: string; color: string }, hint?: string) => (
      <Box key={key} flexDirection="column" alignItems="center" paddingY={SPACE.block} minWidth={0}>
        {glyph && <Text color={glyph.color}>{glyph.mark}</Text>}
        <Text size={SIZE.meta} color={TONE.muted} wrap="wrap">
          {text}
        </Text>
        {hint !== undefined && (
          <Text size={SIZE.meta} {...faint} wrap="wrap">
            {hint}
          </Text>
        )}
      </Box>
    )
    const b = await read($, board)
    if (b === null) {
      return notice('loading', 'Loading workers…')
    }
    const v = await read($, view)
    const checksRead = await read($, checks)
    const deliveredRead = await read($, delivered)
    const picksRead = await read($, picks)
    const todoRead = await read($, todo)

    const fold = (id: string, isOpen: boolean) => update($, view, s => ({ ...s, details: { ...s.details, [id]: isOpen } }))
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
        await $.ui.toast(`no text read for ${label(w)}: press Enter in the field to send it`)
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
    const openSession = async (w: Worker) => {
      try {
        // The app's deep link to a session; it ignores it while deep links are off (policy, logged out)
        const ran = await $.process.run(['open', `claude://code/continue?session=${w.id}`])
        if (ran.exitCode !== 0) {
          await $.ui.toast(`could not open ${label(w)}: ${ran.stderr.trim() || `open exited ${ran.exitCode}`}`)
        }
      } catch (err) {
        await $.ui.toast(`could not open ${label(w)}: ${errorText(err)}`)
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
      return update($, picks, m => ({ ...m, [w.id]: picked(m[w.id]) })).catch((err: unknown) => $.ui.toast(`pick not kept: ${errorText(err)}`))
    }
    /** Writes the person's picks for the worker's mod to answer its dialog with (`dev desktop answer`). */
    const answer = async (w: Worker, a: Ask, p: Picks) => {
      const missing = a.questions.find(q => (p.picks[q.question] ?? []).length === 0)
      if (missing !== undefined) {
        await $.ui.toast(`pick an answer to "${missing.question}" first`)
        return
      }
      if (answering.has(w.id)) return
      const answers = answersOf(a, p)
      if (a.reply) {
        // A wrap_up ask: the worker's own mod submits the pick as the person's reply, else the boss relays it as a message
        answering.add(w.id)
        const text = Object.values(answers).join('; ')
        const isSent = await (a.listening ? replyDirect(w, a, text) : send($, ask('message', w, text), `answer for ${label(w)}`)).finally(() => answering.delete(w.id))
        const sentAt = a.listening ? await $.clock.now() : undefined
        if (isSent) await update($, picks, m => ({ ...m, [w.id]: { ...p, isSent: true, sentAt } })).catch((err: unknown) => $.ui.toast(`reply not marked sent: ${errorText(err)}`))
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
        await $.ui.toast(`${label(w)} not answered: ${why}`)
        return
      }
      await $.ui.toast(`answered ${label(w)}: ${Object.values(answers).join('; ')}`)
      await update($, picks, m => ({ ...m, [w.id]: { ...p, isSent: true } }))
    }
    /** Hands the reply to the worker's mod; whether it was written. */
    const replyDirect = async (w: Worker, a: Ask, text: string) => {
      const why = await writeReply($, w.id, a.toolUseId, text)
      await $.ui.toast(why === '' ? `replied to ${label(w)}: ${text}` : `reply for ${label(w)} not sent: ${why}`)
      return why === ''
    }
    const archive = async (w: Worker) => {
      if (!canArchive(w)) {
        await $.ui.toast(`${label(w)} not archived: ${w.state === 'busy' ? 'running a turn' : `waiting${w.statusText === '' ? '' : ` (${w.statusText})`}`}`)
        return
      }
      // Checked again: the last check may be a minute old
      const got = await deliveredCheck((argv, init) => $.process.run(argv, init), w.worktree)
      if (!got.isDelivered) {
        await $.ui.toast(`${label(w)} not archived: ${got.why}`)
        return
      }
      await send($, ask('archive', w, 'dev delivered exited 0'), `archive of ${label(w)}`)
    }

    /** The question a worker waits on: each question's header and text, its options as buttons in one wrapping row, or one per row with its description beside it where any has one, and Send where it takes more than one press; unanswerable from here, a note that the options open its session. */
    const questionRows = (w: Worker, a: Ask, color: string) => {
      const held = picksFor(a, picksRead[w.id])
      if (held?.isSent) {
        return (
          <Text key={`${w.id}:sent`} size={SIZE.meta} color={TONE.muted}>
            sent: {Object.values(answersOf(a, held)).join('; ')}; waiting for the worker
          </Text>
        )
      }

      return (
        <Box key={`${w.id}:ask`} flexDirection="column" rowGap={SPACE.line} minWidth={0}>
          {a.questions.map((q, qi) => {
            const chosen = held?.picks[q.question] ?? []
            const mark = (isChosen: boolean) => {
              if (isOnePress(a)) return ''
              if (q.multiSelect) return isChosen ? '☑ ' : '☐ '
              return isChosen ? '◉ ' : '○ '
            }
            const isDescribed = q.options.some(o => o.description !== '')
            // One press: the recommended option leads (else the first), as in the app's own next-step box; toggles: the picked ones
            const lead = Math.max(0, q.options.findIndex(o => RECOMMENDED.test(o.label)))

            return (
              <Box key={`${w.id}:q${qi}`} flexDirection="column" rowGap={SPACE.line} minWidth={0}>
                <Box flexDirection="column" minWidth={0}>
                  {q.header !== '' && (
                    <Text size={small} color={color} weight={WEIGHT.strong}>
                      {q.header}
                    </Text>
                  )}
                  <Text weight={WEIGHT.strong}>{q.question}</Text>
                </Box>
                {q.options.length > 0 && (
                  <Box key={`${w.id}:q${qi}:options`} flexDirection={isDescribed ? 'column' : 'row'} flexWrap={isDescribed ? undefined : 'wrap'} columnGap={SPACE.inline} rowGap={SPACE.line} minWidth={0}>
                    {q.options.map((o, oi) => {
                      const isChosen = chosen.includes(o.label)
                      // Without the worker's mod watching, only its own dialog answers: the option opens it
                      const press = () => void (a.answerable || a.reply ? pick(w, a, q.question, o.label, q.multiSelect) : openSession(w))
                      const isPrimary = isOnePress(a) ? oi === lead : isChosen
                      // The pick sent stays the option's own label, suffix included
                      const shown = o.label.replace(RECOMMENDED, '')
                      const button = <Button key={`${w.id}:q${qi}:o${oi}:pick`} label={`${mark(isChosen)}${shown === o.label ? '' : '★ '}${shown}`} variant={isPrimary ? 'primary' : undefined} onPress={press} />
                      if (!isDescribed) return button
                      return (
                        <Box key={`${w.id}:q${qi}:o${oi}`} flexDirection="row" columnGap={SPACE.inline} alignItems="center" minWidth={0}>
                          {button}
                          {o.description !== '' && (
                            <Text key={`${w.id}:q${qi}:o${oi}:about`} size={small} color={TONE.muted} wrap="truncate-end">
                              {o.description}
                            </Text>
                          )}
                        </Box>
                      )
                    })}
                  </Box>
                )}
              </Box>
            )
          })}
          {!a.answerable && !a.reply && (
            <Text size={SIZE.meta} color={TONE.muted}>
              answered in its session: an option opens it
            </Text>
          )}
          {a.reply && a.questions.every(q => q.options.length === 0) && Input !== undefined && (
            <Text size={SIZE.meta} color={TONE.muted}>
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

    /** An icon in a cell of its own: its Svg, named by `alt`, where the surface draws one, else its glyph. */
    const iconCell = (key: string, i: Icon, alt: string) => (
      <Box key={key} flexShrink={0} alignSelf="center">
        {Svg !== undefined ? (
          <Svg source={i.svg} alt={alt} width={ICON_PX} height={ICON_PX} />
        ) : (
          <Text size="caption" color={i.color}>
            {i.glyph}
          </Text>
        )}
      </Box>
    )

    /** A PR's state icon and, while open, its CI rollup's (`PR #93 open`, `CI passed`). */
    const prIcons = (w: Worker, pr: PR, rollup = ciRollup(pr, checksRead[pr.url])) => {
      const state = PR_ICONS[pr.state]
      return [
        state && iconCell(`${w.id}:${pr.url}:state`, state, `PR #${pr.number} ${pr.state.toLowerCase()}`),
        rollup && iconCell(`${w.id}:${pr.url}:ci`, CI_ICONS[rollup], `CI ${rollup}`),
      ]
    }

    /** A PR on the meta line: its state icon, link, CI icon, then the names of the checks that make it, or why they could not be read, cut to fit. */
    const prPart = (w: Worker, pr: PR) => {
      const c = checksRead[pr.url]
      const rollup = ciRollup(pr, c)
      const why = pr.state === 'OPEN' && c !== undefined ? c.error : ''
      const after = rollup && c ? ciNames(c, rollup) : why
      const [state, ci] = prIcons(w, pr, rollup)
      return (
        <Box key={`${w.id}:${pr.url}`} flexDirection="row" alignItems="center" columnGap={SPACE.inline} flexShrink={1} minWidth={0}>
          {state}
          <Box flexShrink={0}>
            <Text size={SIZE.meta}>
              <Link href={pr.url} label={`PR #${pr.number}`} />
            </Text>
          </Box>
          {ci}
          {after !== '' && (
            <Box flexShrink={1} minWidth={0}>
              <Text size={SIZE.meta} color={TONE.muted} dimColor={!isDesktop && why !== ''} wrap="truncate-end">
                {after}
              </Text>
            </Box>
          )}
        </Box>
      )
    }

    /** A step's row: its mark and its text in cells of their own, the text cut to one line; a count of steps looks as they do. */
    const stepRow = (key: string, status: Step['status'], text: string, color: string) => {
      const look = STEP_LOOKS[status] ?? STEP_LOOKS.pending
      const isDim = !isDesktop && look.isDim
      return (
        <Box key={key} flexDirection="row" columnGap={SPACE.inline} minWidth={0}>
          <Box flexShrink={0}>
            <Text size={SIZE.meta} color={look.isDim && isDesktop ? TONE.muted : (look.markColor ?? color)} dimColor={isDim}>
              {look.mark}
            </Text>
          </Box>
          <Box flexShrink={1} minWidth={0}>
            <Text size={SIZE.meta} color={look.isDim && isDesktop ? TONE.muted : look.color} weight={look.weight === 'medium' ? medium : look.weight} dimColor={isDim} wrap="truncate-end">
              {text}
            </Text>
          </Box>
        </Box>
      )
    }

    /** The task progress: a small bar (done steps green on a faint track) and `done/total`. */
    const progress = (w: Worker, done: number) => {
      const cells = Math.round((done / w.plan.length) * BAR_CELLS)
      return (
        <Box key={`${w.id}:progress`} flexDirection="row" alignItems="center" columnGap={SPACE.inline} flexShrink={0}>
          {e.surface === 'terminal' ? (
            // Whole cells: a fifth of a row would draw nothing
            <Box key={`${w.id}:bar`}>
              <Text>
                <Text color="success">{'━'.repeat(cells)}</Text>
                <Text color="inactive" dimColor>
                  {'─'.repeat(BAR_CELLS - cells)}
                </Text>
              </Text>
            </Box>
          ) : (
            <Box key={`${w.id}:bar`} flexDirection="row" alignItems="center" width={BAR_CELLS} height={1}>
              <Box flexDirection="row" flexGrow={1} height="20%" backgroundColor={BAR_TRACK}>
                <Box flexGrow={done} backgroundColor="success" />
                <Box flexGrow={w.plan.length - done} />
              </Box>
            </Box>
          )}
          <Text size={SIZE.meta} color={TONE.muted} tabular>
            {`${done}/${w.plan.length}`}
          </Text>
        </Box>
      )
    }

    /** The task list in full: a row per step, folded past MAX_STEPS. */
    const taskList = (w: Worker, color: string) => {
      const { earlier, steps, more } = shownSteps(w.plan)
      return (
        <Box key={`${w.id}:plan`} flexDirection="column" minWidth={0}>
          {earlier > 0 && stepRow(`${w.id}:earlier`, 'completed', `${earlier} done`, color)}
          {steps.map(s => stepRow(`${w.id}:step${w.plan.indexOf(s)}`, s.status, s.content, color))}
          {more > 0 && stepRow(`${w.id}:more`, 'pending', `${more} more`, color)}
        </Box>
      )
    }

    /** A to do item's row: its box (☐; ◌ waiting on someone else; ☑ ticked, its text struck through), its text cut to one line, then who it waits on, else its link (a PR's icon and number). */
    const todoRow = (it: TodoItem) => {
      const pr = it.link?.match(PR_LINK)?.[1]
      const [box, boxColor] = it.done ? ['☑', 'success'] : it.on ? ['◌', 'subtle'] : ['☐', 'subtle']
      return (
        <Box key={`todo:item${it.id}`} flexDirection="row" alignItems="center" columnGap={SPACE.inline} minWidth={0}>
          <Box flexShrink={0}>
            <Text size={SIZE.meta} color={boxColor}>
              {box}
            </Text>
          </Box>
          <Box flexGrow={1} flexShrink={1} minWidth={0}>
            <Text color={it.done ? (isDesktop ? TONE.muted : 'inactive') : it.on ? TONE.muted : undefined} strikethrough={it.done} wrap="truncate-end">
              {it.text}
            </Text>
          </Box>
          {it.on ? (
            <Box flexShrink={1} minWidth={0}>
              <Text size={SIZE.meta} color={TONE.caution} wrap="truncate-end">
                {`on ${it.on}`}
              </Text>
            </Box>
          ) : (
            it.link && (
              <Box flexDirection="row" alignItems="center" columnGap={SPACE.inline} flexShrink={0}>
                {pr && PR_ICONS.OPEN && iconCell(`todo:item${it.id}:pr`, PR_ICONS.OPEN, `PR #${pr}`)}
                <Text size={SIZE.meta}>
                  <Link href={it.link} label={pr ? `#${pr}` : '↗'} />
                </Text>
              </Box>
            )
          )}
        </Box>
      )
    }

    /** The to do list above the sections: its header (count, how many wait on others, how long ago it changed, dim once stale), the first TODO_SHOWN items, then `+N more`. */
    const todoBlock = (t: Todo) => {
      const open = t.items.filter(it => !it.done)
      const onOthers = open.filter(it => it.on).length
      const shownItems = t.items.slice(0, TODO_SHOWN)
      return (
        <Box key="todo" flexDirection="column" rowGap={SPACE.line} minWidth={0}>
          <Box key="todo:head" flexDirection="row" columnGap={SPACE.inline} minWidth={0}>
            <Text size={SIZE.meta} weight={WEIGHT.strong}>
              TO DO
            </Text>
            <Text size={SIZE.meta} color={TONE.muted} tabular>
              {open.length}
            </Text>
            {onOthers > 0 && (
              <Text size={SIZE.meta} color={TONE.muted}>
                {`· ${onOthers} on others`}
              </Text>
            )}
            <Box flexGrow={1} />
            <Text size={SIZE.meta} color={TONE.muted} dimColor={!isDesktop && t.stale} tabular>
              {`updated ${t.age}`}
            </Text>
          </Box>
          {shownItems.map(todoRow)}
          {t.items.length > shownItems.length && (
            <Text key="todo:more" size={SIZE.meta} color={TONE.muted}>
              {`+${t.items.length - shownItems.length} more`}
            </Text>
          )}
        </Box>
      )
    }

    // A stopped worker's dot is dimmer than an idle one's
    const dot = (w: Worker, group: (typeof GROUPS)[number]) => <Text color={w.state === 'stopped' && !isDesktop ? 'inactive' : DOT_COLORS[group]}>●</Text>
    const sinceOf = (w: Worker) => (w.since === '0m' ? 'now' : w.since)
    /**
     * The worker's name, the button that opens or closes its detail; no label, as the desktop draws a label in place of the children.
     * A quiet one (the open idle line's) is muted and regular on the desktop, so it doesn't compete with the rows asking for the person.
     */
    const nameButton = (w: Worker, isOpen: boolean, isQuiet = false) => (
      <Button key={`${w.id}:fold`} plain onPress={() => void fold(w.id, !isOpen)}>
        <Text weight={isQuiet && isDesktop ? WEIGHT.regular : WEIGHT.strong} {...(isQuiet && isDesktop ? { color: TONE.muted } : {})} wrap="truncate-end">
          {label(w)}
        </Text>
      </Button>
    )
    /** The thin rule between two rows: a faint hairline where the surface draws one, a blank row on the terminal. */
    const rule = (key: string) =>
      e.surface === 'terminal' ? (
        <Box key={key} height={1} />
      ) : (
        <Box key={key} flexDirection="row" alignItems="center" height={1} minWidth={0}>
          <Box flexGrow={1} height="8%" backgroundColor={BAR_TRACK} />
        </Box>
      )

    /** One worker's row: the dot beside line 1 (name, what it says, buttons, time), then its question, meta, warnings, message field and detail. */
    const row = (w: Worker, group: (typeof GROUPS)[number], isOpen: boolean) => {
      const color = sectionColor(group)
      const done = w.plan.filter(s => s.status === 'completed').length
      const delivery = canArchive(w) ? deliveredRead[w.id] : undefined
      const isAttention = ATTENTION_GROUPS.includes(group)
      const lastLines = w.lastLines.filter(line => !isFence(line) && !isMarker(line) && !repeatsTile(line, w))
      // The question's own rows say it while it waits; once sent, line 1 does
      const isAsking = w.ask != null && !picksFor(w.ask, picksRead[w.id])?.isSent
      const says = isAsking ? '' : plainLine(w.ask?.questions[0]?.question || statusLine(w, group) || w.detail || (lastLines.at(-1) ?? ''))
      const since = sinceOf(w)
      // The model and its effort, the context use, each PR, the task progress: `opus·medium · 9% · ○ PR #93 ✓ · ━━─── 2/5`
      const model = w.model && w.effort ? `${w.model}·${w.effort}` : (w.model ?? '')
      const meta = [
        model !== '' && (
          <Text key={`${w.id}:model`} size={SIZE.meta} color={TONE.muted}>
            {model}
          </Text>
        ),
        w.context !== null && (
          <Text key={`${w.id}:context`} size={SIZE.meta} color={TONE.muted} tabular>
            {`${w.context}%`}
          </Text>
        ),
        ...w.prs.map(pr => prPart(w, pr)),
        w.plan.length > 0 && progress(w, done),
      ].filter(part => part !== false)
      const buttons = [
        <Button key={`${w.id}:open`} label="Open" plain dimColor={!isDesktop} onPress={() => void openSession(w)} />,
        Input !== undefined && <Button key={`${w.id}:message`} label="Message…" plain dimColor={!isDesktop} onPress={() => void compose(w.id)} />,
        delivery?.isDelivered === true && <Button key={`${w.id}:archive`} label="Archive" plain dimColor={!isDesktop} onPress={() => void archive(w)} />,
      ]
      const warn = (key: string, text: string) => (
        <Text key={key} size={SIZE.meta} color={TONE.caution} weight={medium}>
          ⚠ {text}
        </Text>
      )

      return (
        <Box key={`${w.id}:row`} flexDirection="row" alignItems="flex-start" columnGap={SPACE.inline} minWidth={0}>
          {dot(w, group)}
          <Box key={`${w.id}:rows`} flexDirection="column" rowGap={SPACE.line} flexGrow={1} flexShrink={1} minWidth={0}>
            {/* Never wraps: the name and what it says are cut, the buttons and time kept at the right */}
            <Box key={`${w.id}:header`} flexDirection="row" alignItems="center" columnGap={SPACE.inline} minWidth={0}>
              <Box flexShrink={1} minWidth={0}>
                {nameButton(w, isOpen)}
              </Box>
              <Box flexGrow={1} flexShrink={1} minWidth={0}>
                {says !== '' && (
                  <Text size={SIZE.meta} color={group === 'Needs you' ? TONE.danger : TONE.muted} weight={group === 'Needs you' ? WEIGHT.strong : WEIGHT.regular} wrap="truncate-end">
                    {says}
                  </Text>
                )}
              </Box>
              {isAttention && (
                <Box flexDirection="row" columnGap={SPACE.inline} flexShrink={0}>
                  {buttons}
                </Box>
              )}
              {since !== '' && (
                <Box flexShrink={0}>
                  <Text size={SIZE.meta} color={TONE.muted} tabular>
                    {since}
                  </Text>
                </Box>
              )}
            </Box>
            {w.ask && questionRows(w, w.ask, color)}
            {meta.length > 0 && (
              <Box key={`${w.id}:meta`} flexDirection="row" flexWrap="wrap" alignItems="center" columnGap={SPACE.inline} minWidth={0}>
                {meta.flatMap((part, i) =>
                  i === 0
                    ? [part]
                    : [
                        <Text key={`${w.id}:dot${i}`} size={SIZE.meta} color={TONE.muted}>
                          ·
                        </Text>,
                        part,
                      ],
                )}
              </Box>
            )}
            {w.notes.map(n => warn(n.key, `${n.kind}: ${n.text}`))}
            {showsNeeds(w) && warn(`${w.id}:needs`, `needs: ${w.needsAction}`)}
            {Input !== undefined && v.composing === w.id && (
              <Box key={`${w.id}:compose`} flexDirection="row" flexWrap="wrap" alignItems="center" columnGap={SPACE.inline} minWidth={0}>
                {/* Its label names the field; a one-line field does not grow on the desktop, so no placeholder to hold */}
                <Input
                  key={`${w.id}:draft`}
                  label={`Message ${label(w)}`}
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
            {/* Its error state: a text given back after a refusal (the toast said why) says what to do */}
            {isDesktop && v.composing === w.id && v.unsent[w.id] !== undefined && (
              <Text key={`${w.id}:unsent`} size={SIZE.meta} color={TONE.caution}>
                not sent: press Send to try again, or Cancel
              </Text>
            )}
            {isOpen && (
              <Box key={`${w.id}:detail`} flexDirection="column" rowGap={SPACE.line} minWidth={0}>
                {w.name !== '' && w.title !== '' && w.title !== w.name && (
                  <Text key={`${w.id}:title`} size={SIZE.meta} color={TONE.muted} wrap="truncate-end">
                    {w.title}
                  </Text>
                )}
                {w.branch !== '' && (
                  // Cut at its start, so its unique end shows
                  <Text size={small} color={TONE.muted} mono wrap="truncate-start">
                    {w.branch}
                  </Text>
                )}
                {w.plan.length > 0 && taskList(w, color)}
                {lastLines.slice(-MAX_LAST_LINES).map((line, i) => (
                  <Text key={`${w.id}:line${i}`} size={SIZE.meta} {...faint} wrap="truncate-end">
                    {plainLine(line)}
                  </Text>
                ))}
                {/* No reply line left to show (e.g. only a marker): its turn's recap says what it did */}
                {lastLines.length === 0 && (w.recap ?? '') !== '' && (
                  <Text key={`${w.id}:recap`} size={SIZE.meta} {...faint} wrap="wrap">
                    {plainLine(w.recap ?? '')}
                  </Text>
                )}
                {delivery?.isDelivered === false && (
                  <Text size={SIZE.meta} color={TONE.muted}>
                    not delivered: {delivery.why}
                  </Text>
                )}
                {!isAttention && (
                  <Box key={`${w.id}:actions`} flexDirection="row" flexWrap="wrap" columnGap={SPACE.inline}>
                    {buttons}
                  </Box>
                )}
              </Box>
            )}
          </Box>
        </Box>
      )
    }

    /** The idle workers not asking for the person, in one line (`▸ a, b, c`; `▸ 2 more · a, b` below rows of the section's own) that opens to a line each: dot, name, task count, time. */
    const idleList = (workers: Worker[], sectionCount: number) => (
      <Box key="idle" flexDirection="column" rowGap={SPACE.line} minWidth={0}>
        <Box flexDirection="row" minWidth={0}>
          <Button key="idle:fold" plain dimColor={!isDesktop} onPress={() => void update($, view, s => ({ ...s, idleOpen: !s.idleOpen }))}>
            <Text size={SIZE.meta} {...(isDesktop ? { color: TONE.muted } : {})} wrap="truncate-end">
              {/* Open on the desktop, its lines below name them: the line counts them only */}
              {v.idleOpen && isDesktop
                ? `▾ ${workers.length} ${workers.length === sectionCount ? 'idle' : 'more'}`
                : `${v.idleOpen ? '▾' : '▸'} ${workers.length === sectionCount ? '' : `${workers.length} more · `}${workers.map(label).join(', ')}`}
            </Text>
          </Button>
        </Box>
        {v.idleOpen &&
          workers.map(w => {
            const done = w.plan.filter(s => s.status === 'completed').length
            const since = sinceOf(w)
            return (
              <Box key={`${w.id}:header`} flexDirection="row" alignItems="center" columnGap={SPACE.inline} minWidth={0}>
                {dot(w, 'Idle')}
                <Box flexShrink={1} minWidth={0}>
                  {nameButton(w, false, true)}
                </Box>
                <Box flexGrow={1} />
                {w.plan.length > 0 && (
                  <Text size={SIZE.meta} color={TONE.muted} tabular>
                    {`${done}/${w.plan.length}`}
                  </Text>
                )}
                {since !== '' && (
                  <Text size={SIZE.meta} color={TONE.muted} tabular>
                    {since}
                  </Text>
                )}
              </Box>
            )
          })}
      </Box>
    )

    // A status a newer dev adds shows under Working rather than not at all
    const sections = GROUPS.map(group => ({ group, workers: b.workers.filter(w => (Object.hasOwn(GROUP_OF, w.status) ? GROUP_OF[w.status] : 'Working') === group) }))
      .filter(s => s.workers.length > 0)
    /** Whether a worker gets a full row: any but Idle, or an idle one asking for the person, being messaged or opened. */
    const isFull = (w: Worker, group: (typeof GROUPS)[number]) => group !== 'Idle' || asksForPerson(w) || v.composing === w.id || v.details[w.id] === true

    return (
      <Box flexDirection="column" gap={isDesktop ? SPACE.section : SPACE.block}>
        {b.error !== '' && notice('error', b.error, { mark: '⚠', color: 'error' }, b.error === NOT_DESKTOP ? undefined : `retries every ${PERIOD_MS / 1000} s`)}
        {b.warnings !== '' && (
          <Box key="warnings" flexDirection="column" minWidth={0}>
            {b.warnings.split('\n').filter(line => line.trim() !== '').map((line, i) => (
              <Box key={`warning${i}`} flexDirection="row" columnGap={SPACE.inline} minWidth={0}>
                <Text size={SIZE.meta} color={TONE.caution}>
                  ⚠
                </Text>
                <Box flexShrink={1} minWidth={0}>
                  <Text size={SIZE.meta} color={TONE.muted} wrap="wrap">
                    {line.replace(WARNING_PREFIX, '')}
                  </Text>
                </Box>
              </Box>
            ))}
          </Box>
        )}
        {todoRead !== null && todoBlock(todoRead)}
        {b.error === '' && b.workers.length === 0 && notice('empty', 'No workers yet', { mark: '○', color: 'subtle' }, 'start_session from this boss session adds one')}
        {sections.map(({ group, workers }) => {
          const quiet = workers.filter(w => !isFull(w, group))
          const items = [...workers.filter(w => isFull(w, group)).map(w => row(w, group, v.details[w.id] === true)), ...(quiet.length > 0 ? [idleList(quiet, workers.length)] : [])]
          return (
            <Box key={group} flexDirection="column" rowGap={SPACE.line}>
              <Box flexDirection="row" columnGap={SPACE.inline}>
                <Text size={SIZE.meta} weight={WEIGHT.strong} color={sectionColor(group)}>
                  {group.toUpperCase()}
                </Text>
                <Text size={SIZE.meta} color={TONE.muted} tabular>
                  {workers.length}
                </Text>
              </Box>
              {items.flatMap((item, i) => (i === 0 ? [item] : [rule(`${group}:rule${i}`), item]))}
            </Box>
          )
        })}
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
    if (!b.workers.some(w => w.status === NEEDS_YOU)) return <Text dimColor>{line}</Text>
    // Only what asks for the person is red: `1 needs you`, the rest dim
    const [needsYou, ...rest] = line.split(' · ')

    return (
      <Text>
        <Text color="error">{needsYou}</Text>
        <Text dimColor>{` · ${rest.join(' · ')}`}</Text>
      </Text>
    )
  })
}

import { atom, read, update } from 'claude-code'
import type { Register } from 'claude-code'

import type { Board, Checks, PR, Worker } from '../types'

// The dev binary with `dev desktop board`: an absolute path when the app's PATH lacks it
const DEV_BIN = 'dev'

const PANE = 'board'
const PERIOD_MS = 2000
// PR checks and `dev delivered` (a git fetch, gh) are read at most this often per PR or worker
const REFRESH_MS = 60_000
const NEEDS_YOU = 'needs you'
const NUDGE = 'Continue where you left off'
const STATE_COLORS = { busy: 'success', blocked: 'warning', idle: 'suggestion', stopped: 'inactive' } as const
const STATUS_COLORS: Record<string, string> = {
  'needs you': 'error',
  'rate-limited': 'warning',
  stuck: 'warning',
  working: 'success',
  waiting: 'suggestion',
  blocked: 'warning',
  'review ready': 'merged',
  idle: 'inactive',
  stopped: 'inactive',
}
const PR_COLORS: Record<string, string> = { OPEN: 'success', MERGED: 'merged', CLOSED: 'error' }
const STEP_ICONS: Record<string, string> = { completed: '✅', in_progress: '🔄', pending: '◌' }
const CHECK_ICONS: Record<string, string> = { pass: '✅', fail: '❌', pending: '⏳', skipping: '⏭', cancel: '⊘' }

type Run = (argv: string[], init?: { timeoutMs?: number; stdin?: string }) => Promise<{ exitCode: number; stdout: string; stderr: string }>

// Change the tag when Board's, Worker's, PR's, Note's or Step's fields change: the atom then declines what an earlier version stored
const board = atom({ plugin: 'dev-board', key: 'board' } as const, null, { shape: 'board-3' })
// The keys of the notes already sent to the boss, newest last, so a reload sends none again
const sent = atom({ plugin: 'dev-board', key: 'sent' } as const, [], { shape: 'sent-1' })
const MAX_SENT = 500
// By PR URL
const checks = atom({ plugin: 'dev-board', key: 'checks' } as const, {}, { shape: 'checks-1' })
// By worker id
const delivered = atom({ plugin: 'dev-board', key: 'delivered' } as const, {}, { shape: 'delivered-1' })
const view = atom({ plugin: 'dev-board', key: 'view' } as const, { expanded: [], composing: '' }, { shape: 'view-1' })

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
      Array.isArray(w?.prs) && Array.isArray(w?.lastLines) && Array.isArray(w?.notes) && Array.isArray(w?.plan) && typeof w?.status === 'string'
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

/** What a button asks the boss, e.g. `dev-board: nudge fix-login (local_1): Continue where you left off`. */
const ask = (verb: 'message' | 'nudge' | 'archive', w: Worker, text: string) => `dev-board: ${verb} ${label(w)} (${w.id}): ${text}`

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

/** How long a worker has been in its state, e.g. `busy 12m`, `idle 3m ago`. */
function age(w: Worker): string {
  if (w.since === '') return w.state
  return w.state === 'busy' ? `busy ${w.since}` : `${w.state} ${w.since} ago`
}

/** The status line, e.g. `needs you · allow Bash: rm -rf bin`, `waiting 4m · Auto-fix: PR #80`. */
function statusLine(w: Worker): string {
  const head = w.status === 'waiting' && w.since !== '' ? `waiting ${w.since}` : w.status
  return w.statusText === '' ? head : `${head} · ${w.statusText}`
}

/** A PR's CI line, e.g. `✅ build ❌ test`, or why it has none; '' for a PR not open or not read yet. */
function ciLine(pr: PR, c: Checks | undefined): string {
  if (pr.state !== 'OPEN' || c === undefined) return ''
  if (c.error !== '') return c.error
  return c.checks.map(k => `${CHECK_ICONS[k.bucket] ?? k.bucket} ${k.name}`).join(' ')
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({ name: 'board', description: "Show this boss session's workers in a pane" })
    const boss = await $.env.get('CLAUDE_CODE_HOST_SESSION_ID')
    if (!boss) {
      const error = 'Not a desktop app session: CLAUDE_CODE_HOST_SESSION_ID is unset.'
      await update($, board, () => ({ workers: [], error, warnings: '' }))
      return next(e)
    }
    const run: Run = (argv, init) => $.process.run(argv, init)
    let isPolling = false
    let last = ''
    // The PR URLs and worker ids being read: a slow gh or fetch must not stack another
    const reading = new Set<string>()
    /** Reads what is due (not read for REFRESH_MS) of the open PRs' checks and the idle PR workers' delivery. */
    const refresh = async (workers: Worker[]) => {
      const now = await $.clock.now()
      const due = (at: number | undefined, key: string) => !reading.has(key) && (at === undefined || now - at >= REFRESH_MS)
      const readChecks = await read($, checks)
      for (const pr of workers.flatMap(w => w.prs.filter(pr => pr.state === 'OPEN'))) {
        if (due(readChecks[pr.url]?.at, pr.url)) {
          reading.add(pr.url)
          void prChecks(run, pr.url)
            .then(got => update($, checks, m => ({ ...m, [pr.url]: { ...got, at: now } })))
            .finally(() => reading.delete(pr.url))
        }
      }
      const readDelivered = await read($, delivered)
      for (const w of workers.filter(w => w.prs.length > 0 && w.state !== 'busy' && w.worktree !== '')) {
        if (due(readDelivered[w.id]?.at, w.id)) {
          reading.add(w.id)
          void run([DEV_BIN, 'delivered', w.worktree], { timeoutMs: 60_000 })
            .then(
              ran => ran.exitCode === 0,
              () => false,
            )
            .then(isDelivered => update($, delivered, m => ({ ...m, [w.id]: { at: now, isDelivered } })))
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
        }
        await refresh(polled.workers)
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

  on('command.run', { command: 'board' }, async $ => {
    const opened = await $.ui.open({ id: PANE, title: 'Workers' })

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
    // Needs you first, the rest oldest first
    const workers = [...b.workers].sort((x, y) => Number(y.status === NEEDS_YOU) - Number(x.status === NEEDS_YOU))

    /** Sends the boss a prompt a button asks for, saying in a toast whether it was queued. */
    const send = (text: string, what: string) =>
      $.prompt.submit({ text }).then(
        r => $.ui.toast(r.drop === undefined ? `dev-board: ${what} queued for the boss` : `dev-board: ${what} not sent: ${r.drop}`),
        (err: unknown) => $.ui.toast(`dev-board: ${what} not sent: ${errorText(err)}`),
      )
    const toggle = (id: string) =>
      update($, view, s => ({ ...s, expanded: s.expanded.includes(id) ? s.expanded.filter(x => x !== id) : [...s.expanded, id] }))
    const compose = (id: string) => update($, view, s => ({ ...s, composing: s.composing === id ? '' : id }))
    const copy = async (id: string, surface: typeof e.surface) => {
      // A remote surface (the desktop app) has no clipboard path yet: the Mac's then
      const copied = (await $.ui.copy({ text: id, surface })).isCopied || (await $.process.run(['pbcopy'], { stdin: id })).exitCode === 0
      await $.ui.toast(copied ? `Copied ${id}` : `dev-board: could not copy ${id}`)
    }
    const openSession = async (w: Worker) => {
      // The app's deep link to a session; it ignores it while deep links are off (policy, logged out)
      const ran = await $.process.run(['open', `claude://code/continue?session=${w.id}`])
      if (ran.exitCode !== 0) {
        await $.ui.toast(`dev-board: could not open ${label(w)}: ${ran.stderr.trim() || `open exited ${ran.exitCode}`}`)
      }
    }

    return (
      <Box flexDirection="column" gap={1}>
        {b.error !== '' && <Text color="error">{b.error}</Text>}
        {b.warnings !== '' && <Text color="warning">{b.warnings}</Text>}
        {b.error === '' && b.workers.length === 0 && <Text dimColor>No workers.</Text>}
        {workers.map(w => {
          const done = w.plan.filter(s => s.status === 'completed').length
          const isExpanded = v.expanded.includes(w.id)
          const canArchive = w.state !== 'busy' && deliveredRead[w.id]?.isDelivered === true

          return (
            <Box key={w.id} flexDirection="column">
              <Text>
                <Text color={STATE_COLORS[w.state]}>●</Text> <Text weight="bold">{w.title || w.name}</Text>
                {w.plan.length > 0 && <Text dimColor>{` ${done}/${w.plan.length}`}</Text>}
              </Text>
              <Text color={STATUS_COLORS[w.status] ?? 'text'}>{statusLine(w)}</Text>
              <Text dimColor>{[age(w), w.branch, w.context === null ? '' : `context ${w.context}%`].filter(s => s !== '').join(' · ')}</Text>
              {w.prs.map(pr => {
                const c = checksRead[pr.url]
                const ci = ciLine(pr, c)

                return (
                  <Text key={pr.url}>
                    <Link href={pr.url} label={`PR #${pr.number}`} /> <Text color={PR_COLORS[pr.state] ?? 'inactive'}>{pr.state.toLowerCase()}</Text>
                    {ci !== '' && <Text dimColor={c?.error !== ''}>{`  ${ci}`}</Text>}
                  </Text>
                )
              })}
              {w.verdict !== '' && (
                <Text dimColor={w.state === 'busy'}>
                  {w.state === 'busy' ? 'last turn: ' : ''}
                  {w.verdict}
                  {w.detail === '' ? '' : `: ${w.detail}`}
                </Text>
              )}
              {w.notes.map(n => (
                <Text key={n.key} color="warning">
                  {n.kind}: {n.text}
                </Text>
              ))}
              {w.lastLines.map(line => (
                <Text dimColor>{line}</Text>
              ))}
              <Box flexDirection="row" flexWrap="wrap" columnGap={1}>
                {w.plan.length > 0 && <Button key={`${w.id}:tasks`} label={isExpanded ? 'Tasks ▾' : 'Tasks ▸'} dimColor onPress={() => void toggle(w.id)} />}
                <Button key={`${w.id}:open`} label="Open" onPress={() => void openSession(w)} />
                {Input !== undefined && <Button key={`${w.id}:message`} label="Message…" onPress={() => void compose(w.id)} />}
                <Button key={`${w.id}:nudge`} label="Nudge" onPress={() => void send(ask('nudge', w, NUDGE), `nudge for ${label(w)}`)} />
                <Button key={`${w.id}:copy`} label="Copy id" dimColor onPress={press => void copy(w.id, press.surface)} />
                {canArchive && (
                  <Button key={`${w.id}:archive`} label="Archive" onPress={() => void send(ask('archive', w, 'dev delivered exited 0'), `archive of ${label(w)}`)} />
                )}
              </Box>
              {isExpanded &&
                w.plan.map(s => (
                  <Text dimColor={s.status === 'completed'}>
                    {STEP_ICONS[s.status] ?? '◌'} {s.content}
                  </Text>
                ))}
              {Input !== undefined && v.composing === w.id && (
                <Input
                  key={`${w.id}:draft`}
                  placeholder={`Message to ${label(w)}, relayed by the boss`}
                  submitLabel="send"
                  autoFocus
                  onSubmit={text => {
                    void update($, view, s => ({ ...s, composing: '' }))
                    if (text.trim() !== '') {
                      void send(ask('message', w, text.trim()), `message for ${label(w)}`)
                    }
                  }}
                />
              )}
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

    return b.workers.some(w => w.status === NEEDS_YOU) ? <Text color="error">{line}</Text> : <Text dimColor>{line}</Text>
  })
}

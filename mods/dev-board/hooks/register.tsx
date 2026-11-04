import { atom, read, update } from 'claude-code'
import type { Register } from 'claude-code'

import type { Board, Worker } from '../types'

// The dev binary with `dev desktop board`: an absolute path when the app's PATH lacks it
const DEV_BIN = 'dev'

const PANE = 'board'
const PERIOD_MS = 2000
const STATE_COLORS = { busy: 'success', blocked: 'warning', idle: 'suggestion', stopped: 'inactive' } as const
const PR_COLORS: Record<string, string> = { OPEN: 'success', MERGED: 'merged', CLOSED: 'error' }

// Change the tag when Board's fields change: it declines what an earlier version stored
const board = atom({ plugin: 'dev-board', key: 'board' } as const, null, { shape: 'board-1' })

/** Runs `dev desktop board` once: its workers, or its error text. */
async function poll(run: (argv: string[]) => Promise<{ exitCode: number; stdout: string; stderr: string }>, boss: string): Promise<Board> {
  try {
    const ran = await run([DEV_BIN, 'desktop', 'board', '--json', '--boss', boss])
    const stderr = ran.stderr.trim()
    if (ran.exitCode !== 0) {
      return { workers: [], error: stderr || `${DEV_BIN} desktop board exited ${ran.exitCode}`, warnings: '' }
    }
    return { workers: JSON.parse(ran.stdout) as Worker[], error: '', warnings: stderr }
  } catch (err) {
    const why = err instanceof Error ? err.message : String(err)
    return { workers: [], error: `${DEV_BIN} desktop board: ${why}`, warnings: '' }
  }
}

/** The band's line, e.g. `3 workers · 1 busy · 1 blocked · PR #78 open`. */
function bandLine(workers: Worker[]): string {
  const count = (state: Worker['state']) => workers.filter(w => w.state === state).length
  const open = workers.flatMap(w => w.prs.filter(pr => pr.state === 'OPEN').map(pr => `#${pr.number}`))
  const parts = [`${workers.length} worker${workers.length === 1 ? '' : 's'}`]
  for (const state of ['busy', 'blocked'] as const) {
    if (count(state) > 0) {
      parts.push(`${count(state)} ${state}`)
    }
  }
  if (open.length > 0) {
    parts.push(`${open.length === 1 ? 'PR' : 'PRs'} ${open.join(' ')} open`)
  }
  return parts.join(' · ')
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
    let isPolling = false
    let last = ''
    const tick = async () => {
      // A slow run must not stack another on top
      if (isPolling) return
      isPolling = true
      try {
        const polled = await poll(argv => $.process.run(argv), boss)
        const json = JSON.stringify(polled)
        if (json !== last) {
          last = json
          await update($, board, () => polled)
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
    const { Box, Text, Link } = $.ui.resolve(e)
    const b = await read($, board)
    if (b === null) {
      return <Text dimColor>Loading workers…</Text>
    }

    return (
      <Box flexDirection="column" gap={1}>
        {b.error !== '' && <Text color="error">{b.error}</Text>}
        {b.warnings !== '' && <Text color="warning">{b.warnings}</Text>}
        {b.error === '' && b.workers.length === 0 && <Text dimColor>No workers.</Text>}
        {b.workers.map(w => (
          <Box key={w.id} flexDirection="column">
            <Text>
              <Text color={STATE_COLORS[w.state]}>●</Text> <Text weight="bold">{w.title || w.name}</Text>
            </Text>
            <Text dimColor>
              {[w.state, w.branch, w.context === null ? '' : `context ${w.context}%`].filter(s => s !== '').join(' · ')}
            </Text>
            {w.prs.map(pr => (
              <Text key={pr.url}>
                <Link href={pr.url} label={`PR #${pr.number}`} />{' '}
                <Text color={PR_COLORS[pr.state] ?? 'inactive'}>{pr.state.toLowerCase()}</Text>
              </Text>
            ))}
            {w.verdict !== '' && (
              <Text dimColor={w.state === 'busy'}>
                {w.state === 'busy' ? 'last turn: ' : ''}
                {w.verdict}
                {w.detail === '' ? '' : `: ${w.detail}`}
              </Text>
            )}
            {w.needsAction !== '' && w.state !== 'busy' && <Text color="warning">needs: {w.needsAction}</Text>}
            {w.tool !== '' && <Text color="claude">running {w.tool}</Text>}
            {w.lastLines.map(line => (
              <Text dimColor>{line}</Text>
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

    return <Text dimColor>{bandLine(b.workers)}</Text>
  })
}

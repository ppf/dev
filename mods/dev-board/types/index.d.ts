/** A pull request the app tracks for a worker. */
export type PR = { number: number; url: string; state: string }

/** One object of `dev desktop board --json`. */
export type Worker = {
  id: string
  name: string
  title: string
  branch: string
  worktree: string
  prs: PR[]
  state: 'busy' | 'blocked' | 'idle' | 'stopped'
  createdAt: number
  lastActivityAt: number
  verdict: string
  detail: string
  needsAction: string
  lastLines: string[]
  tool: string
  context: number | null
}

/** The last poll: its workers, or why it failed; warnings are its stderr. */
export type Board = { workers: Worker[]; error: string; warnings: string }

declare module 'claude-code' {
  interface PluginState {
    'dev-board': { board: Shaped<Board | null> }
  }
}

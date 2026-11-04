/** A pull request the app tracks for a worker. */
export type PR = { number: number; url: string; state: string }

/** A health note on a worker; `key` stays the same while its condition holds. */
export type Note = { key: string; kind: string; text: string }

/** A step of a worker's task list (its update_plan calls replayed). */
export type Step = { content: string; status: 'completed' | 'in_progress' | 'pending' }

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
  notes: Note[]
  status: string
  statusText: string
  since: string
  plan: Step[]
}

/** The last poll: its workers, or why it failed or did not run; warnings are its stderr. */
export type Board = { workers: Worker[]; error: string; warnings: string }

/** One CI check of a PR, as `gh pr checks --json name,bucket` has it. */
export type Check = { name: string; bucket: string }

/** A PR's checks as last read (`at`, the clock's ms), or why they could not be. */
export type Checks = { at: number; checks: Check[]; error: string }

/** Whether `dev delivered` last passed (`at`, the clock's ms) for a worker's worktree. */
export type Delivered = { at: number; isDelivered: boolean }

/** The pane's own state: the workers whose task list is expanded, the one a message is typed for. */
export type View = { expanded: string[]; composing: string }

declare module 'claude-code' {
  interface PluginState {
    'dev-board': {
      board: Shaped<Board | null>
      sent: Shaped<string[]>
      checks: Shaped<Record<string, Checks>>
      delivered: Shaped<Record<string, Delivered>>
      view: Shaped<View>
    }
  }
}

/** A pull request the app tracks for a worker. */
export type PR = { number: number; url: string; state: string }

/** A health note on a worker; `key` stays the same while its condition holds. */
export type Note = { key: string; kind: string; text: string }

/** A step of a worker's task list (its update_plan calls replayed). */
export type Step = { content: string; status: 'completed' | 'in_progress' | 'pending' }

/** A worker's status, finer than its state; the first that holds (`internal/desktop/status.go`). */
export type Status = 'needs you' | 'rate-limited' | 'stuck' | 'working' | 'waiting' | 'blocked' | 'review ready' | 'idle' | 'stopped'

/** One of an AskUserQuestion's questions; `description` may be empty. */
export type Question = { question: string; header: string; options: { label: string; description: string }[]; multiSelect: boolean }

/** The question a busy worker waits on: from its mod's file (`answerable` from the board), else its transcript (its own dialog only). */
export type Ask = { toolUseId: string; questions: Question[]; askedAt: number; answerable: boolean }

/** The person's picks on a worker's question, by question text; `isSent` once `dev desktop answer` took them. */
export type Picks = { toolUseId: string; picks: Record<string, string[]>; isSent: boolean }

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
  status: Status
  statusText: string
  since: string
  plan: Step[]
  /** Absent from a dev older than this mod. */
  ask?: Ask | null
}

/** The last poll: its workers, or why it failed or did not run; warnings are its stderr. */
export type Board = { workers: Worker[]; error: string; warnings: string }

/** One CI check of a PR, as `gh pr checks --json name,bucket` has it. */
export type Check = { name: string; bucket: string }

/** A PR's checks as last read (`at`, the clock's ms), or why they could not be. */
export type Checks = { at: number; checks: Check[]; error: string }

/** Whether `dev delivered` last passed (`at`, the clock's ms) for a worker's worktree, else why not. */
export type Delivered = { at: number; isDelivered: boolean; why: string }

/** The pane's own state: expanded task lists, the worker a message is being typed for, tiles the person (un)folded (true open; beats the default), the text of a message not sent by worker id (drawn back into its field). */
export type View = { expanded: string[]; composing: string; tiles: Record<string, boolean>; unsent: Record<string, string> }

declare module 'claude-code' {
  interface PluginState {
    'dev-board': {
      board: Shaped<Board | null>
      sent: Shaped<string[]>
      checks: Shaped<Record<string, Checks>>
      delivered: Shaped<Record<string, Delivered>>
      view: Shaped<View>
      bossAsked: Shaped<boolean>
      picks: Shaped<Record<string, Picks>>
    }
  }
}

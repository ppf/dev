import type { EngineInterface } from 'claude-code'

import type { Fleet, FleetAgent, FleetItem, FleetThread } from '../types'

type Run = EngineInterface['process']['run']

/** A tmux boss session's name without its `boss-` prefix, the project. */
export const projectOf = (boss: string) => boss.replace(/^boss-/, '')

/** The `dev todo answer` argv a board button runs: `--from-board` is the way past the Claude Code refusal (the board runs inside one), `--question` guards a reused item number, `--` keeps a label starting with `-` an argument. */
export const answerArgv = (bin: string, it: FleetItem, option: string) => [bin, 'todo', 'answer', '--from-board', '--question', it.text, '--', it.boss, String(it.id), option]

const readJson = async (run: Run, argv: string[]): Promise<unknown> => {
  const ran = await run(argv, { timeoutMs: 10_000 })
  if (ran.exitCode !== 0) throw new Error(ran.stderr.trim() || `${argv.slice(0, 3).join(' ')} exited ${ran.exitCode}`)
  return JSON.parse(ran.stdout)
}

const isArray = (v: unknown): v is any[] => Array.isArray(v)
const num = (v: unknown, field: string): number | undefined => {
  if (v === undefined) return undefined
  if (typeof v !== 'number') throw new Error(`dev agent list --json printed a ${field} this mod does not read`)
  return v
}

/** Every tmux boss's items waiting on the person (not `--on` anyone), from `dev todo --all --json`, and each list it couldn't read into `errors`; throws on output it does not read. */
export async function readAsks(run: Run, bin: string, errors: string[] = []): Promise<FleetItem[]> {
  const lists = await readJson(run, [bin, 'todo', '--all', '--json'])
  if (!isArray(lists)) throw new Error('dev todo --all --json printed no array')
  return lists.flatMap(l => {
    if (typeof l?.boss !== 'string' || !isArray(l.items)) throw new Error('dev todo --all --json printed a list this mod does not read')
    if (typeof l.error === 'string' && l.error !== '') errors.push(`${l.boss}: ${l.error}`)
    return l.items.flatMap((it: any): FleetItem[] => {
      if (typeof it?.id !== 'number' || typeof it.text !== 'string' || typeof it.done !== 'boolean') throw new Error('dev todo --all --json printed an item this mod does not read')
      if (it.on) return []
      return [{ boss: l.boss, id: it.id, text: it.text, done: it.done, options: isArray(it.options) ? it.options.map(String) : [], answer: typeof it.answer === 'string' ? it.answer : undefined }]
    })
  })
}

/** The tmux bosses, and the workers waiting on the person, from `dev agent list --json`. */
export async function readAgents(run: Run, bin: string): Promise<FleetAgent[]> {
  const agents = await readJson(run, [bin, 'agent', 'list', '--json'])
  if (!isArray(agents)) throw new Error('dev agent list --json printed no array')
  return agents.flatMap((a): FleetAgent[] => {
    if (typeof a?.session !== 'string') throw new Error('dev agent list --json printed an agent this mod does not read')
    const isBoss = a.boss === true
    if (!isBoss && a.deciding !== true) return []
    return [{ session: a.session, project: String(a.project ?? ''), isBoss, state: String(a.state ?? ''), model: String(a.model ?? ''), effort: String(a.effort ?? ''), cost: num(a.cost_total_usd, 'cost_total_usd') ?? num(a.cost_usd, 'cost_usd'), closed: num(a.cost_closed_usd, 'cost_closed_usd'), costError: typeof a.cost_error === 'string' ? a.cost_error : undefined }]
  })
}

/** The open message threads from `dev agent messages --json`. */
export async function readThreads(run: Run, bin: string): Promise<FleetThread[]> {
  const threads = await readJson(run, [bin, 'agent', 'messages', '--json'])
  if (threads === null) return []
  if (!isArray(threads)) throw new Error('dev agent messages --json printed no array')
  return threads.flatMap((t): FleetThread[] => {
    if (typeof t?.id !== 'string' || !isArray(t.messages)) throw new Error('dev agent messages --json printed a thread this mod does not read')
    if (t.state !== 'open') return []
    return [{ id: t.id, messages: t.messages.map((m: any) => ({ id: String(m.id), from: String(m.from), to: String(m.to), text: String(m.text), at: Number(m.at) })) }]
  })
}

/** The three reads, each failing alone: what failed is missing from the fleet and its cause in `errors`. */
export async function readFleet(run: Run, bin: string): Promise<{ fleet: Fleet; errors: string[] }> {
  const errors: string[] = []
  const take = <T,>(read: () => Promise<T[]>) =>
    read().catch((err: unknown) => {
      errors.push(err instanceof Error ? err.message : String(err))
      return [] as T[]
    })
  const [asks, agents, threads] = await Promise.all([take(() => readAsks(run, bin, errors)), take(() => readAgents(run, bin)), take(() => readThreads(run, bin))])
  return { fleet: { asks, agents, threads }, errors }
}

/** `$3.80`. */
export const cost = (usd: number) => `$${usd.toFixed(2)}`

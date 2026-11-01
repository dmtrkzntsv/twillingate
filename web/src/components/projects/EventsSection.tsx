import { useQuery } from '@tanstack/react-query'
import { BanIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { EventFamily, EventName, EventRule, Project } from '@/lib/api'
import { formatExact } from '@/lib/format'
import { eventNamesQuery, eventRulesQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'
import LoadError from './LoadError'
import { AliasBadges, RuleDialogs, RuleMenu, useRuleDialogs } from './RuleDialogs'

interface Props {
  project: Project
  /** The range the page shows: the names received in it. */
  range: { from: string; to: string }
  pending?: boolean
  /** Each resolves true when done; a refusal is already toasted (except a rename's 'conflict', which points to merging). */
  onRename: (body: { family: EventFamily; from: string; to: string }) => Promise<boolean | 'conflict'>
  onMerge: (body: { family: EventFamily; from: string; to: string }) => Promise<boolean>
  onBlock: (body: { family: EventFamily; name: string }) => Promise<boolean>
  onRemove: (body: { family: EventFamily; name: string }) => Promise<boolean>
  onAddAlias: (body: { family: EventFamily; alias: string; to: string }) => Promise<boolean>
}

/** A dialog's target: the name, with its family after it. */
type Target = { name: string; context: EventFamily }

/** A listed event with the rules on it: blocked, and the old names renamed to it. */
type Row = EventName & { blocked: boolean; aliases: string[] }

/**
 * The range's names with their rules, every rule shown on its event: a
 * block as the row's state, a rename as an alias under its target. A
 * blocked name or a rename target with nothing in the range gets a row of
 * 0, so no rule is out of sight. Ordered by family (product first), then
 * name.
 */
function eventRows(list: EventName[], rules: EventRule[]): Row[] {
  const key = (family: EventFamily, name: string) => `${family}/${name}`
  const blocked = new Set(rules.filter((r) => r.action === 'block').map((r) => key(r.family, r.name)))
  const aliases = new Map<string, string[]>()
  for (const r of rules) {
    if (r.action !== 'rename' || !r.to) continue
    const k = key(r.family, r.to)
    aliases.set(k, [...(aliases.get(k) ?? []), r.name])
  }
  const row = (e: EventName): Row => ({ ...e, blocked: blocked.has(key(e.family, e.name)), aliases: aliases.get(key(e.family, e.name)) ?? [] })
  const rows = list.map(row)
  const seen = new Set(list.map((e) => key(e.family, e.name)))
  for (const r of rules) {
    const name = r.action === 'rename' && r.to ? r.to : r.name
    if (seen.has(key(r.family, name))) continue
    seen.add(key(r.family, name))
    rows.push(row({ family: r.family, name, events: 0 }))
  }
  const families: EventFamily[] = ['product', 'measures']
  return rows.sort((a, b) => families.indexOf(a.family) - families.indexOf(b.family) || a.name.localeCompare(b.name))
}

/** A project's event names with what each received and the rules on each: rename, merge and block (both confirmed) and unblock, aliases added and removed. */
export default function EventsSection({ project, range, pending, onRename, onMerge, onBlock, onRemove, onAddAlias }: Props) {
  const id = project.project_id
  const dialogs = useRuleDialogs<Target>()
  const names = useQuery(eventNamesQuery(id, range))
  const rules = useQuery(eventRulesQuery(id))
  const list = names.data?.events ?? []
  const rows = names.data ? eventRows(list, rules.data?.rules ?? []) : []
  // The names with events, and the blocked ones, in a family: what a rename or alias in it must know.
  const takenIn = (family?: EventFamily) => list.filter((e) => e.family === family).map((e) => e.name)
  const blockedIn = (family?: EventFamily) => (rules.data?.rules ?? []).filter((r) => r.action === 'block' && r.family === family).map((r) => r.name)
  return (
    <section aria-label="Events" className="flex flex-col gap-3 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">Events</h2>
      </header>
      {names.data && list.length === 0 && <p className="text-sm text-muted-foreground">No product events or measures in this range.</p>}
      {rows.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow><TableHead>Event</TableHead><TableHead>Received</TableHead><TableHead /></TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((e) => (
              <TableRow key={`${e.family}/${e.name}`} className={cn(e.blocked ? 'bg-destructive/5 hover:bg-destructive/10' : e.aliases.length > 0 && 'bg-sky-500/5 hover:bg-sky-500/10')}>
                <TableCell className="whitespace-normal">
                  <div className="flex flex-wrap items-center gap-2">
                    <code className={cn('text-sm break-all', e.blocked && 'text-muted-foreground line-through')}>
                      <span className="text-xs text-muted-foreground">{e.family}/</span>{e.name}
                    </code>
                    {e.blocked && <Badge variant="outline" className="border-destructive/30 bg-destructive/10 text-destructive"><BanIcon />Blocked</Badge>}
                  </div>
                  {e.aliases.length > 0 && <AliasBadges target={{ name: e.name, context: e.family }} aliases={e.aliases} pending={pending} dialogs={dialogs} />}
                </TableCell>
                <TableCell className="text-sm">
                  {e.blocked ? (
                    <span className="text-muted-foreground">{formatExact(e.events)}<span className="block text-xs">no longer stored</span></span>
                  ) : formatExact(e.events)}
                </TableCell>
                <TableCell className="text-right">
                  <RuleMenu
                    label={`Actions for ${e.name} (${e.family})`}
                    target={{ name: e.name, context: e.family }}
                    blocked={e.blocked}
                    pending={pending}
                    dialogs={dialogs}
                    onUnblock={() => void onRemove({ family: e.family, name: e.name })}
                  />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {names.error && !names.data && <LoadError what="event names" error={names.error} onRetry={() => void names.refetch()} />}
      {rules.error && !rules.data && <LoadError what="event rules" error={rules.error} onRetry={() => void rules.refetch()} />}
      <RuleDialogs
        noun="event"
        dialogs={dialogs}
        pending={pending}
        taken={(t) => takenIn(t?.context)}
        blocked={(t) => blockedIn(t?.context)}
        into={(t) => rows.filter((r) => r.family === t?.context && r.name !== t?.name && !r.blocked).map((r) => ({ name: r.name, count: r.events }))}
        aliases={(t) => rows.find((r) => r.family === t?.context && r.name === t?.name)?.aliases ?? []}
        onRename={(t, to) => onRename({ family: t.context, from: t.name, to })}
        onMerge={(t, to) => onMerge({ family: t.context, from: t.name, to })}
        onAddAlias={(t, alias) => onAddAlias({ family: t.context, alias, to: t.name })}
        onBlock={(t) => onBlock({ family: t.context, name: t.name })}
        onRemove={(t) => onRemove({ family: t.context, name: t.name })}
      />
    </section>
  )
}

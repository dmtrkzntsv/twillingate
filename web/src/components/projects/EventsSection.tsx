import { useQuery } from '@tanstack/react-query'
import { BanIcon, MoreHorizontalIcon, PlusIcon, XIcon } from 'lucide-react'
import ConfirmDialog from '@/components/ConfirmDialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useClosingTarget } from '@/hooks/use-closing-target'
import type { EventFamily, EventName, EventRule, Project } from '@/lib/api'
import { formatExact } from '@/lib/format'
import { eventNamesQuery, eventRulesQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'
import LoadError from './LoadError'
import AliasDialog from './AliasDialog'
import MergeDialog from './MergeDialog'
import RenameDialog from './RenameDialog'
import { ruleCopy } from './rule-copy'

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

type Target = { family: EventFamily; name: string }

const copy = ruleCopy('event')
/** A dialog's target: the name, with its family after it. */
const asRuleTarget = (t: Target | undefined) => (t ? { name: t.name, context: t.family } : null)

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
  const renaming = useClosingTarget<Target>()
  const merging = useClosingTarget<Target>()
  const blocking = useClosingTarget<Target>()
  const aliasing = useClosingTarget<Target>()
  const unaliasing = useClosingTarget<Target & { to: string }>()
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
                  {e.aliases.length > 0 && (
                    <div className="mt-1 flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
                      <span>also from</span>
                      {e.aliases.map((a) => (
                        <Badge key={a} variant="outline" className="gap-0.5 border-sky-500/30 bg-background pr-0.5 font-mono whitespace-normal">
                          <span className="break-all">{a}</span>
                          <Button variant="ghost" size="icon" className="size-4" aria-label={`Remove alias ${a}`} disabled={pending}
                            onClick={() => unaliasing.show({ family: e.family, name: a, to: e.name })}>
                            <XIcon className="size-3" />
                          </Button>
                        </Badge>
                      ))}
                      <Button variant="outline" size="icon" className="size-5 rounded-full" aria-label={`Add alias to ${e.name}`} disabled={pending}
                        onClick={() => aliasing.show({ family: e.family, name: e.name })}>
                        <PlusIcon className="size-3" />
                      </Button>
                    </div>
                  )}
                </TableCell>
                <TableCell className="text-sm">
                  {e.blocked ? (
                    <span className="text-muted-foreground">{formatExact(e.events)}<span className="block text-xs">no longer stored</span></span>
                  ) : formatExact(e.events)}
                </TableCell>
                <TableCell className="text-right">
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="icon" className="size-7" aria-label={`Actions for ${e.name} (${e.family})`}>
                        <MoreHorizontalIcon />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem disabled={pending} onClick={() => renaming.show({ family: e.family, name: e.name })}>Rename…</DropdownMenuItem>
                      <DropdownMenuItem disabled={pending} onClick={() => merging.show({ family: e.family, name: e.name })}>Merge into…</DropdownMenuItem>
                      <DropdownMenuItem disabled={pending} onClick={() => aliasing.show({ family: e.family, name: e.name })}>Add alias…</DropdownMenuItem>
                      {e.blocked ? (
                        <DropdownMenuItem disabled={pending} onClick={() => void onRemove({ family: e.family, name: e.name })}>Unblock</DropdownMenuItem>
                      ) : (
                        <DropdownMenuItem disabled={pending} onClick={() => blocking.show({ family: e.family, name: e.name })}>Block…</DropdownMenuItem>
                      )}
                    </DropdownMenuContent>
                  </DropdownMenu>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {names.error && !names.data && <LoadError what="event names" error={names.error} onRetry={() => void names.refetch()} />}
      {rules.error && !rules.data && <LoadError what="event rules" error={rules.error} onRetry={() => void rules.refetch()} />}
      <RenameDialog
        open={renaming.open}
        onOpenChange={renaming.onOpenChange}
        noun="event"
        target={asRuleTarget(renaming.target)}
        taken={takenIn(renaming.target?.family)}
        blocked={blockedIn(renaming.target?.family)}
        pending={pending}
        onRename={(to) => onRename({ family: renaming.target!.family, from: renaming.target!.name, to })}
      />
      <MergeDialog
        open={merging.open}
        onOpenChange={merging.onOpenChange}
        noun="event"
        target={asRuleTarget(merging.target)}
        into={rows
          .filter((r) => r.family === merging.target?.family && r.name !== merging.target?.name && !r.blocked)
          .map((r) => ({ name: r.name, count: r.events }))}
        pending={pending}
        onMerge={(to) => onMerge({ family: merging.target!.family, from: merging.target!.name, to })}
      />
      <AliasDialog
        open={aliasing.open}
        onOpenChange={aliasing.onOpenChange}
        noun="event"
        target={asRuleTarget(aliasing.target)}
        taken={takenIn(aliasing.target?.family)}
        aliases={rows.find((r) => r.family === aliasing.target?.family && r.name === aliasing.target?.name)?.aliases ?? []}
        pending={pending}
        onAdd={(alias) => onAddAlias({ family: aliasing.target!.family, alias, to: aliasing.target!.name })}
      />
      <ConfirmDialog
        open={unaliasing.open}
        onOpenChange={unaliasing.onOpenChange}
        titleClassName="wrap-anywhere"
        descriptionClassName="wrap-anywhere"
        title={unaliasing.target && copy.unalias.title(unaliasing.target.name)}
        description={unaliasing.target && copy.unalias.description(unaliasing.target.name, unaliasing.target.to)}
        action="Remove alias"
        onAction={() => { const t = unaliasing.target!; void onRemove({ family: t.family, name: t.name }) }}
      />
      <ConfirmDialog
        open={blocking.open}
        onOpenChange={blocking.onOpenChange}
        titleClassName="wrap-anywhere"
        descriptionClassName="wrap-anywhere"
        title={blocking.target && copy.block.title(blocking.target)}
        description={blocking.target && copy.block.description(asRuleTarget(blocking.target)!)}
        action={copy.block.action}
        onAction={() => void onBlock(blocking.target!)}
      />
    </section>
  )
}

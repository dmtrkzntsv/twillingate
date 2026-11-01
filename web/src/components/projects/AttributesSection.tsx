import { useQuery } from '@tanstack/react-query'
import { BanIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { Project } from '@/lib/api'
import { attributesQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'
import { describeKey } from './attributes'
import LoadError from './LoadError'
import { AliasBadges, RuleDialogs, RuleMenu, useRuleDialogs } from './RuleDialogs'
import type { RuleTarget } from './rule-copy'

interface Props {
  project: Project
  /** The range the page shows: what each key received in it. */
  range: { from: string; to: string }
  pending?: boolean
  /** Each resolves true when done; a refusal is already toasted (except a rename's 'conflict', which points to merging). */
  onRename: (body: { from: string; to: string }) => Promise<boolean | 'conflict'>
  onMerge: (body: { from: string; to: string }) => Promise<boolean>
  onBlock: (body: { key: string }) => Promise<boolean>
  onRemove: (body: { key: string }) => Promise<boolean>
  onAddAlias: (body: { alias: string; to: string }) => Promise<boolean>
}

/** list_attributes covers at most this many days (both ends counted) a call. */
const MAX_DAYS = 92

/** The last MAX_DAYS days of the range: the whole of it when it fits. */
function clampRange(range: { from: string; to: string }): { from: string; to: string } {
  const earliest = new Date(Date.parse(`${range.to}T00:00:00Z`) - (MAX_DAYS - 1) * 86_400_000).toISOString().slice(0, 10)
  return range.from < earliest ? { from: earliest, to: range.to } : range
}

/**
 * A project's attribute keys over the range, busiest first, each with what it
 * received and the rules on it: rename, merge and block (both confirmed) and
 * unblock, aliases added and removed. Every key breaks down in the event
 * files, so there is nothing to switch on; rules cover custom keys, so a `$`
 * key has no menu.
 */
export default function AttributesSection({ project, range, pending, onRename, onMerge, onBlock, onRemove, onAddAlias }: Props) {
  const dialogs = useRuleDialogs<RuleTarget>()
  const asked = clampRange(range)
  const clamped = asked.from !== range.from
  const { data, error, refetch } = useQuery(attributesQuery(project.project_id, asked))
  const rows = data?.attributes ?? []
  // The server lists only the busiest received keys; say so when it left some out.
  const receivedListed = rows.filter((r) => r.received).length
  // What a rename or alias must know: the keys with data, and the blocked ones.
  const taken = rows.filter((r) => r.received).map((r) => r.key)
  const blocked = rows.filter((r) => r.blocked).map((r) => r.key)
  return (
    <section aria-label="Attributes" className="flex flex-col gap-3 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">Attributes</h2>
      </header>
      {clamped && (
        <p className="text-xs text-muted-foreground">Showing the last {MAX_DAYS} days of the range ({asked.from} to {asked.to}).</p>
      )}
      {data && rows.length === 0 && <p className="text-sm text-muted-foreground">No attributes received in this range.</p>}
      {rows.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Attribute</TableHead>
              <TableHead className="hidden sm:table-cell">Received</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => {
              const aliases = r.aliases ?? []
              const received = describeKey(r)
              const custom = !r.key.startsWith('$')
              return (
                <TableRow key={r.key} className={cn(r.blocked ? 'bg-destructive/5 hover:bg-destructive/10' : aliases.length > 0 && 'bg-sky-500/5 hover:bg-sky-500/10')}>
                  <TableCell className="whitespace-normal">
                    <div className="flex flex-wrap items-center gap-2">
                      <code className={cn('text-sm break-all', r.blocked && 'text-muted-foreground line-through')}>{r.key}</code>
                      {r.blocked && <Badge variant="outline" className="border-destructive/30 bg-destructive/10 text-destructive"><BanIcon />Blocked</Badge>}
                    </div>
                    <span className="block text-xs text-muted-foreground sm:hidden">{received}</span>
                    {aliases.length > 0 && <AliasBadges target={{ name: r.key }} aliases={aliases} pending={pending} dialogs={dialogs} />}
                  </TableCell>
                  <TableCell className="hidden text-xs whitespace-normal text-muted-foreground sm:table-cell">
                    {received}
                    {r.blocked && <span className="block">no longer stored</span>}
                  </TableCell>
                  <TableCell className="text-right">
                    {custom && (
                      <RuleMenu
                        label={`Actions for ${r.key}`}
                        target={{ name: r.key }}
                        blocked={r.blocked}
                        pending={pending}
                        dialogs={dialogs}
                        onUnblock={() => void onRemove({ key: r.key })}
                      />
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}
      {data && data.keys_total > receivedListed && (
        <p className="text-xs text-muted-foreground">
          Showing the {receivedListed.toLocaleString()} busiest of {data.keys_total.toLocaleString()} keys.
        </p>
      )}
      {error && !data && <LoadError what="attributes" error={error} onRetry={() => void refetch()} />}
      <RuleDialogs
        noun="attribute"
        dialogs={dialogs}
        pending={pending}
        taken={() => taken}
        blocked={() => blocked}
        into={(t) => rows.filter((r) => r.key !== t?.name && !r.blocked && !r.key.startsWith('$')).map((r) => ({ name: r.key, count: r.events }))}
        aliases={(t) => rows.find((r) => r.key === t?.name)?.aliases ?? []}
        onRename={(t, to) => onRename({ from: t.name, to })}
        onMerge={(t, to) => onMerge({ from: t.name, to })}
        onAddAlias={(t, alias) => onAddAlias({ alias, to: t.name })}
        onBlock={(t) => onBlock({ key: t.name })}
        onRemove={(t) => onRemove({ key: t.name })}
      />
    </section>
  )
}

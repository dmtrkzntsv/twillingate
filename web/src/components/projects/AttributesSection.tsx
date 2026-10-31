import { useQuery } from '@tanstack/react-query'
import { BanIcon, MoreHorizontalIcon, PlusIcon, XIcon } from 'lucide-react'
import ConfirmDialog from '@/components/ConfirmDialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useClosingTarget } from '@/hooks/use-closing-target'
import type { Project } from '@/lib/api'
import { attributesQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'
import AliasDialog from './AliasDialog'
import { describeKey } from './attributes'
import LoadError from './LoadError'
import MergeDialog from './MergeDialog'
import RenameDialog from './RenameDialog'
import { ruleCopy } from './rule-copy'

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

const copy = ruleCopy('attribute')

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
  const renaming = useClosingTarget<string>()
  const merging = useClosingTarget<string>()
  const blocking = useClosingTarget<string>()
  const aliasing = useClosingTarget<string>()
  const unaliasing = useClosingTarget<{ key: string; to: string }>()
  const asked = clampRange(range)
  const clamped = asked.from !== range.from
  const { data, error, refetch } = useQuery(attributesQuery(project.project_id, asked))
  const rows = data?.attributes ?? []
  // The server lists only the busiest received keys; say so when it left some out.
  const receivedListed = rows.filter((r) => r.received).length
  // What a rename or alias must know: the keys with data, and the blocked ones.
  const taken = rows.filter((r) => r.received).map((r) => r.key)
  const blocked = rows.filter((r) => r.blocked).map((r) => r.key)
  const target = (k: string | undefined) => (k === undefined ? null : { name: k })
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
                    {aliases.length > 0 && (
                      <div className="mt-1 flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
                        <span>also from</span>
                        {aliases.map((a) => (
                          <Badge key={a} variant="outline" className="gap-0.5 border-sky-500/30 bg-background pr-0.5 font-mono whitespace-normal">
                            <span className="break-all">{a}</span>
                            <Button variant="ghost" size="icon" className="size-4" aria-label={`Remove alias ${a}`} disabled={pending}
                              onClick={() => unaliasing.show({ key: a, to: r.key })}>
                              <XIcon className="size-3" />
                            </Button>
                          </Badge>
                        ))}
                        <Button variant="outline" size="icon" className="size-5 rounded-full" aria-label={`Add alias to ${r.key}`} disabled={pending}
                          onClick={() => aliasing.show(r.key)}>
                          <PlusIcon className="size-3" />
                        </Button>
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="hidden text-xs whitespace-normal text-muted-foreground sm:table-cell">
                    {received}
                    {r.blocked && <span className="block">no longer stored</span>}
                  </TableCell>
                  <TableCell className="text-right">
                    {custom && (
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button variant="ghost" size="icon" className="size-7" aria-label={`Actions for ${r.key}`}>
                            <MoreHorizontalIcon />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem disabled={pending} onClick={() => renaming.show(r.key)}>Rename…</DropdownMenuItem>
                          <DropdownMenuItem disabled={pending} onClick={() => merging.show(r.key)}>Merge into…</DropdownMenuItem>
                          <DropdownMenuItem disabled={pending} onClick={() => aliasing.show(r.key)}>Add alias…</DropdownMenuItem>
                          {r.blocked ? (
                            <DropdownMenuItem disabled={pending} onClick={() => void onRemove({ key: r.key })}>Unblock</DropdownMenuItem>
                          ) : (
                            <DropdownMenuItem disabled={pending} onClick={() => blocking.show(r.key)}>Block…</DropdownMenuItem>
                          )}
                        </DropdownMenuContent>
                      </DropdownMenu>
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
      <RenameDialog
        open={renaming.open}
        onOpenChange={renaming.onOpenChange}
        noun="attribute"
        target={target(renaming.target)}
        taken={taken}
        blocked={blocked}
        pending={pending}
        onRename={(to) => onRename({ from: renaming.target!, to })}
      />
      <MergeDialog
        open={merging.open}
        onOpenChange={merging.onOpenChange}
        noun="attribute"
        target={target(merging.target)}
        into={rows
          .filter((r) => r.key !== merging.target && !r.blocked && !r.key.startsWith('$'))
          .map((r) => ({ name: r.key, count: r.events }))}
        pending={pending}
        onMerge={(to) => onMerge({ from: merging.target!, to })}
      />
      <AliasDialog
        open={aliasing.open}
        onOpenChange={aliasing.onOpenChange}
        noun="attribute"
        target={target(aliasing.target)}
        taken={taken}
        aliases={rows.find((r) => r.key === aliasing.target)?.aliases ?? []}
        pending={pending}
        onAdd={(alias) => onAddAlias({ alias, to: aliasing.target! })}
      />
      <ConfirmDialog
        open={unaliasing.open}
        onOpenChange={unaliasing.onOpenChange}
        titleClassName="wrap-anywhere"
        descriptionClassName="wrap-anywhere"
        title={unaliasing.target && copy.unalias.title(unaliasing.target.key)}
        description={unaliasing.target && copy.unalias.description(unaliasing.target.key, unaliasing.target.to)}
        action="Remove alias"
        onAction={() => void onRemove({ key: unaliasing.target!.key })}
      />
      <ConfirmDialog
        open={blocking.open}
        onOpenChange={blocking.onOpenChange}
        titleClassName="wrap-anywhere"
        descriptionClassName="wrap-anywhere"
        title={blocking.target !== undefined && copy.block.title({ name: blocking.target })}
        description={blocking.target !== undefined && copy.block.description({ name: blocking.target })}
        action={copy.block.action}
        onAction={() => void onBlock({ key: blocking.target! })}
      />
    </section>
  )
}

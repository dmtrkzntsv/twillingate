import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { BanIcon, MoreHorizontalIcon, PlusIcon, XIcon } from 'lucide-react'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
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

/**
 * A project's attribute keys over the range, busiest first, each with what it
 * received and the rules on it: rename, merge and block (both confirmed) and
 * unblock, aliases added and removed. Every key breaks down in the event
 * files, so there is nothing to switch on; rules cover custom keys, so a `$`
 * key has no menu.
 */
export default function AttributesSection({ project, range, pending, onRename, onMerge, onBlock, onRemove, onAddAlias }: Props) {
  // The keys outlive their dialogs' close animations, so a title does not flash "Block undefined?".
  const [renaming, setRenaming] = useState<{ key: string; open: boolean } | null>(null)
  const [merging, setMerging] = useState<{ key: string; open: boolean } | null>(null)
  const [blocking, setBlocking] = useState<{ key: string; open: boolean } | null>(null)
  const [aliasing, setAliasing] = useState<{ key: string; open: boolean } | null>(null)
  const [unaliasing, setUnaliasing] = useState<{ key: string; to: string; open: boolean } | null>(null)
  const { data, error, refetch } = useQuery(attributesQuery(project.project_id, range))
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
                              onClick={() => setUnaliasing({ key: a, to: r.key, open: true })}>
                              <XIcon className="size-3" />
                            </Button>
                          </Badge>
                        ))}
                        <Button variant="outline" size="icon" className="size-5 rounded-full" aria-label={`Add alias to ${r.key}`} disabled={pending}
                          onClick={() => setAliasing({ key: r.key, open: true })}>
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
                          <DropdownMenuItem disabled={pending} onClick={() => setRenaming({ key: r.key, open: true })}>Rename…</DropdownMenuItem>
                          <DropdownMenuItem disabled={pending} onClick={() => setMerging({ key: r.key, open: true })}>Merge into…</DropdownMenuItem>
                          <DropdownMenuItem disabled={pending} onClick={() => setAliasing({ key: r.key, open: true })}>Add alias…</DropdownMenuItem>
                          {r.blocked ? (
                            <DropdownMenuItem disabled={pending} onClick={() => void onRemove({ key: r.key })}>Unblock</DropdownMenuItem>
                          ) : (
                            <DropdownMenuItem disabled={pending} onClick={() => setBlocking({ key: r.key, open: true })}>Block…</DropdownMenuItem>
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
        open={renaming?.open === true}
        onOpenChange={(o) => !o && setRenaming((d) => d && { ...d, open: false })}
        noun="attribute"
        target={target(renaming?.key)}
        taken={taken}
        blocked={blocked}
        pending={pending}
        onRename={(to) => onRename({ from: renaming!.key, to })}
      />
      <MergeDialog
        open={merging?.open === true}
        onOpenChange={(o) => !o && setMerging((d) => d && { ...d, open: false })}
        noun="attribute"
        target={target(merging?.key)}
        into={rows
          .filter((r) => r.key !== merging?.key && !r.blocked && !r.key.startsWith('$'))
          .map((r) => ({ name: r.key, count: r.events }))}
        pending={pending}
        onMerge={(to) => onMerge({ from: merging!.key, to })}
      />
      <AliasDialog
        open={aliasing?.open === true}
        onOpenChange={(o) => !o && setAliasing((d) => d && { ...d, open: false })}
        noun="attribute"
        target={target(aliasing?.key)}
        taken={taken}
        aliases={rows.find((r) => r.key === aliasing?.key)?.aliases ?? []}
        pending={pending}
        onAdd={(alias) => onAddAlias({ alias, to: aliasing!.key })}
      />
      <AlertDialog open={unaliasing?.open === true} onOpenChange={(o) => !o && setUnaliasing((d) => d && { ...d, open: false })}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="wrap-anywhere">{unaliasing && copy.unalias.title(unaliasing.key)}</AlertDialogTitle>
            <AlertDialogDescription className="wrap-anywhere">{unaliasing && copy.unalias.description(unaliasing.key, unaliasing.to)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void onRemove({ key: unaliasing!.key })}>Remove alias</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog open={blocking?.open === true} onOpenChange={(o) => !o && setBlocking((d) => d && { ...d, open: false })}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="wrap-anywhere">{blocking && copy.block.title({ name: blocking.key })}</AlertDialogTitle>
            <AlertDialogDescription className="wrap-anywhere">{blocking && copy.block.description({ name: blocking.key })}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => { const k = blocking!.key; setBlocking({ key: k, open: false }); void onBlock({ key: k }) }}>{copy.block.action}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

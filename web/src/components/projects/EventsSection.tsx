import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { MoreHorizontalIcon } from 'lucide-react'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { EventFamily, Project } from '@/lib/api'
import { formatExact } from '@/lib/format'
import { eventNamesQuery, eventRulesQuery } from '@/lib/queries'
import LoadError from './LoadError'
import RenameEventDialog from './RenameEventDialog'

interface Props {
  project: Project
  /** The range the page shows: the names received in it. */
  range: { from: string; to: string }
  /** RETENTION_RAW_DAYS, for the block dialog. */
  rawDays?: number
  pending?: boolean
  /** Each resolves true when done; a refusal is already toasted (except a rename's 'conflict', which asks to merge). */
  onRename: (body: { family: EventFamily; from: string; to: string; merge?: boolean }) => Promise<boolean | 'conflict'>
  onBlock: (body: { family: EventFamily; name: string }) => Promise<boolean>
  onRemove: (body: { family: EventFamily; name: string }) => Promise<boolean>
}

type Target = { family: EventFamily; name: string }

/** A project's event names with what each received: rename (or merge) and block (confirmed), and the rules in force. */
export default function EventsSection({ project, range, rawDays, pending, onRename, onBlock, onRemove }: Props) {
  const id = project.project_id
  // The targets outlive their dialogs' close animations, so a title does not flash "Block undefined?".
  const [renaming, setRenaming] = useState<{ target: Target; open: boolean } | null>(null)
  const [blocking, setBlocking] = useState<{ target: Target; open: boolean } | null>(null)
  const names = useQuery(eventNamesQuery(id, range))
  const rules = useQuery(eventRulesQuery(id))
  const list = names.data?.events ?? []
  const blocked = new Set((rules.data?.rules ?? []).filter((r) => r.action === 'block').map((r) => `${r.family}/${r.name}`))
  return (
    <section aria-label="Events" className="flex flex-col gap-3 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">Events</h2>
      </header>
      {names.data && list.length === 0 && <p className="text-sm text-muted-foreground">No product events or measures in this range.</p>}
      {list.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow><TableHead>Event</TableHead><TableHead>Received</TableHead><TableHead /></TableRow>
          </TableHeader>
          <TableBody>
            {list.map((e) => (
              <TableRow key={`${e.family}/${e.name}`}>
                <TableCell className="whitespace-normal">
                  <code className="text-sm break-all">{e.name}</code>
                  <div className="flex items-center gap-2 text-xs text-muted-foreground">
                    <span>{e.family}</span>
                    {blocked.has(`${e.family}/${e.name}`) && <Badge variant="outline">blocked</Badge>}
                  </div>
                </TableCell>
                <TableCell className="text-sm">{formatExact(e.events)}</TableCell>
                <TableCell className="text-right">
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="ghost" size="icon" className="size-7" aria-label={`Actions for ${e.name} (${e.family})`}>
                        <MoreHorizontalIcon />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem disabled={pending} onClick={() => setRenaming({ target: { family: e.family, name: e.name }, open: true })}>Rename…</DropdownMenuItem>
                      {!blocked.has(`${e.family}/${e.name}`) && (
                        <DropdownMenuItem disabled={pending} onClick={() => setBlocking({ target: { family: e.family, name: e.name }, open: true })}>Block…</DropdownMenuItem>
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
      {(rules.data?.rules.length ?? 0) > 0 && (
        <div className="flex flex-col gap-1">
          <h3 className="text-sm font-medium">Rules</h3>
          <ul className="flex flex-col">
            {rules.data!.rules.map((r) => (
              <li key={`${r.family}/${r.name}`} className="flex items-center justify-between gap-2 text-sm">
                <span className="min-w-0 break-all">
                  <span>{r.action === 'rename' ? `${r.name} → ${r.to}` : `${r.name} blocked`}</span>
                  <span className="text-xs text-muted-foreground"> · {r.family}</span>
                </span>
                <Button variant="ghost" size="sm" aria-label={`Remove rule for ${r.name}`} disabled={pending} onClick={() => void onRemove({ family: r.family, name: r.name })}>Remove</Button>
              </li>
            ))}
          </ul>
        </div>
      )}
      {rules.error && !rules.data && <LoadError what="event rules" error={rules.error} onRetry={() => void rules.refetch()} />}
      <RenameEventDialog
        open={renaming?.open === true}
        onOpenChange={(o) => !o && setRenaming((d) => d && { ...d, open: false })}
        target={renaming?.target ?? null}
        names={list}
        blockedNames={(rules.data?.rules ?? []).filter((r) => r.action === 'block' && r.family === renaming?.target.family).map((r) => r.name)}
        pending={pending}
        onRename={(to, merge) => onRename({ family: renaming!.target.family, from: renaming!.target.name, to, ...(merge ? { merge: true } : {}) })}
      />
      <AlertDialog open={blocking?.open === true} onOpenChange={(o) => !o && setBlocking((d) => d && { ...d, open: false })}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="break-all">Block {blocking?.target.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Events named {blocking?.target.name} ({blocking?.target.family}) are dropped when they arrive. The next daily pass (03:00 UTC) deletes its aggregated history;
              stored raw events expire within {rawDays ? `${rawDays} days` : 'the raw retention window'}. Remove the rule to receive it again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => { const t = blocking!.target; setBlocking({ target: t, open: false }); void onBlock(t) }}>Block event</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

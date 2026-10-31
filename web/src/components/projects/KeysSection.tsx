import { useState } from 'react'
import ConfirmDialog from '@/components/ConfirmDialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useClosingTarget } from '@/hooks/use-closing-target'
import type { IngestKey, IssuedKey } from '@/lib/api'
import CopyButton from './CopyButton'
import IssueKeyDialog from './IssueKeyDialog'
import LoadError from './LoadError'

interface Props {
  /** Undefined until the keys have loaded. */
  keys: IngestKey[] | undefined
  /** Set when reading them failed and there is nothing to show instead. */
  error?: Error | null
  onRetry?: () => void
  onIssue: (label: string) => Promise<IssuedKey | undefined>
  onDisable: (label: string) => Promise<unknown>
  onEnable: (label: string) => Promise<unknown>
  pending?: boolean
}

/** A project's ingest keys: copy, issue, disable (confirmed) and enable. */
export default function KeysSection({ keys, error, onRetry, onIssue, onDisable, onEnable, pending }: Props) {
  const [issuing, setIssuing] = useState(false)
  const disabling = useClosingTarget<string>()
  return (
    <section aria-label="Ingest keys" className="flex flex-col gap-3 rounded-lg border p-4">
      <header className="flex items-center justify-between">
        <h2 className="text-base font-semibold">Ingest keys</h2>
        <Button variant="outline" size="sm" onClick={() => setIssuing(true)}>Issue key</Button>
      </header>
      {error && !keys ? (
        <LoadError what="keys" error={error} onRetry={() => onRetry?.()} />
      ) : !keys ? (
        <Skeleton aria-hidden className="h-16 w-full" />
      ) : keys.length === 0 ? (
        <p className="text-sm text-muted-foreground">No keys: this project can receive nothing.</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow><TableHead>Label</TableHead><TableHead>Key</TableHead><TableHead /></TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((k) => (
              <TableRow key={k.label} aria-label={k.label}>
                {/* The state sits by the label, not in a column of its own, so the row fits a phone. */}
                <TableCell className="whitespace-normal">
                  <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="font-medium break-all">{k.label}</span>
                    <Badge variant={k.state === 'active' ? 'secondary' : 'outline'}>{k.state}</Badge>
                  </span>
                </TableCell>
                <TableCell>
                  <span className="flex items-center gap-1 font-mono text-xs">
                    <span className="max-w-20 truncate sm:max-w-48">{k.key}</span>
                    <CopyButton value={k.key} label={`Copy ${k.label}`} />
                  </span>
                </TableCell>
                <TableCell className="text-right">
                  {k.state === 'active' ? (
                    <Button variant="ghost" size="sm" aria-label={`Disable ${k.label}`} disabled={pending} onClick={() => disabling.show(k.label)}>Disable</Button>
                  ) : (
                    <Button variant="ghost" size="sm" aria-label={`Enable ${k.label}`} disabled={pending} onClick={() => void onEnable(k.label)}>Enable</Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <IssueKeyDialog open={issuing} onOpenChange={setIssuing} onIssue={onIssue} pending={pending} />
      <ConfirmDialog
        open={disabling.open}
        onOpenChange={disabling.onOpenChange}
        title={<>Disable {disabling.target}?</>}
        description="Events sent with it are rejected within a second, from every site that uses it. You can enable it again."
        action="Disable key"
        onAction={() => void onDisable(disabling.target!)}
      />
    </section>
  )
}

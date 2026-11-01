import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import type { IngestKey, Project } from '@/lib/api'
import IssuedKeyView from './IssuedKeyView'
import LoadError from './LoadError'

interface Props {
  project: Project
  /** Undefined until the keys have loaded. */
  keys: IngestKey[] | undefined
  /** Set when reading them failed and there is nothing to show instead. */
  error?: Error | null
  onRetry?: () => void
  /** The server's word on the snippets, when they carry a placeholder collector. */
  note?: string
}

/**
 * How to add the script by hand: the snippet of the project's first active
 * key, behind a button, with what would stop it from working.
 */
export default function InstallSection({ project, keys, error, onRetry, note }: Props) {
  const [open, setOpen] = useState(false)
  const key = keys?.find((k) => k.state === 'active')
  return (
    <section aria-label="Install" className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-4">
      <div className="flex min-w-0 flex-col gap-1">
        <h2 className="text-base font-semibold">Install</h2>
        <p className="text-sm text-muted-foreground">Add the tracking script to your site by hand.</p>
      </div>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>Show install snippet</Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="break-words">Install on {project.name}</DialogTitle>
            <DialogDescription>Paste this into the <code className="font-mono text-xs">&lt;head&gt;</code> of every page.</DialogDescription>
          </DialogHeader>
          {error && !keys ? (
            <LoadError what="keys" error={error} onRetry={() => onRetry?.()} />
          ) : !keys ? (
            <Skeleton aria-hidden className="h-16 w-full" />
          ) : key ? (
            <>
              <IssuedKeyView snippet={key.snippet} note={note} />
              <p className="text-xs text-muted-foreground">It uses the key <span className="font-medium break-all">{key.label}</span>.</p>
            </>
          ) : (
            <p role="status" className="text-sm text-destructive">This project has no active key, so there is nothing to paste yet. Issue or enable one under Ingest keys.</p>
          )}
          {project.allowed_origins.length === 0 && (
            <p role="status" className="text-sm text-destructive">No allowed origins: browsers' events are refused until you add your site under Allowed origins.</p>
          )}
        </DialogContent>
      </Dialog>
    </section>
  )
}

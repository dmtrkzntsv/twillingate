import { useId } from 'react'
import { describeSource } from '@/lib/alerts'
import type { Alert } from '@/lib/api'

/**
 * What an alert watches: the source in words, and for an alert no template
 * made, its query as JSON under a closed **Query** (a silence alert's event
 * filters), read-only. The JSON scrolls inside its own box, never the page.
 * A template alert's source line stands alone; its fields are the editor's.
 * A broken alert (no kind) shows its stored query whatever made it, since
 * its template line would describe what no longer holds.
 */
export default function AlertDefinition({ alert }: { alert: Alert }) {
  const headingId = useId()
  const query = alert.kind === 'silence' ? (alert.where ?? []) : alert.query
  return (
    <section aria-labelledby={headingId} className="flex min-w-0 flex-col gap-2">
      <h3 id={headingId} className="text-base font-semibold">
        Watches
      </h3>
      <p className="text-sm break-words">{describeSource(alert)}</p>
      {(!alert.template || alert.kind === '') && (
        <details className="min-w-0 text-sm">
          <summary className="text-muted-foreground">Query</summary>
          <pre className="mt-2 max-w-full overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs">{JSON.stringify(query, null, 2)}</pre>
        </details>
      )}
    </section>
  )
}

import { useQuery } from '@tanstack/react-query'
import CodeBlock from '@/components/projects/CodeBlock'
import { formSnippet } from '@/lib/forms'
import { keysQuery } from '@/lib/queries'

interface Props {
  projectId: number
  /** list_forms' action_base: where a plain HTML form posts; empty without INGEST_URL. */
  actionBase: string
  /** The form the snippets post to. */
  name: string
  /** Its fields, one control each in the tagged form; `email` and `message` when empty. */
  fields?: string[]
}

/**
 * How a page sends to form `name`: a tagged form for a page with the SDK,
 * and the URL a plain HTML form posts to (on the collector list_forms'
 * action_base names), with the project's active key.
 */
export default function FormSnippets({ projectId, actionBase, name, fields }: Props) {
  const keysQ = useQuery(keysQuery(projectId))
  const key = keysQ.data?.keys.find((k) => k.state === 'active')?.key ?? 'ak_…'
  // Without INGEST_URL the server cannot name the collector: a placeholder the text explains.
  const action = `${actionBase || 'https://<collector>/ingest/forms'}/${name}?key=${key}`
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <p className="max-w-prose text-sm text-muted-foreground">On a page with the twillingate script, tag the form:</p>
      <CodeBlock value={formSnippet(name, fields)} label="Copy form snippet" />
      <p className="max-w-prose text-sm text-muted-foreground">
        Without the script, point a plain form's <code className="font-mono text-xs">action</code> at the collector, the address your install snippet
        loads <code className="font-mono text-xs">twillingate.js</code> from:
      </p>
      <CodeBlock value={action} label="Copy action URL" />
    </div>
  )
}

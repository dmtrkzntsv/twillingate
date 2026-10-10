import type { CompletionContext, CompletionResult } from '@codemirror/autocomplete'
import type { SchemaTable } from './schema-views'

/** The views and their columns, as lang-sql's schema option takes them. */
export function sqlNamespace(tables: SchemaTable[]): Record<string, string[]> {
  return Object.fromEntries(tables.map((t) => [t.name, t.columns]))
}

const params = [
  { label: ':project', detail: 'the project shown' },
  { label: ':from', detail: 'first day, YYYY-MM-DD' },
  { label: ':to', detail: 'last day, YYYY-MM-DD' },
]

/** :project, :from and :to, the only parameters a widget gets. */
export function paramCompletions(context: CompletionContext): CompletionResult | null {
  const word = context.matchBefore(/:\w*/)
  if (!word) return null
  return { from: word.from, options: params.map((p) => ({ ...p, type: 'variable' })), validFor: /^:\w*$/ }
}

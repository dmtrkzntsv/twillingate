/** A queryable table and its columns, for the SQL editor's completion. */
export interface SchemaTable {
  name: string
  columns: string[]
}

const line = /^\s+([a-z_][a-z0-9_]*)\(([^)]*)\)/i

/** The line that opens SQLite's deprecated views, which widget SQL no longer reads. */
const deprecated = 'Deprecated: query with source sqlite'

/**
 * Reads the event files' tables out of `GET /api/schema/views`, whose
 * reference lists each as an indented `name(col, col, …)` line followed by
 * an optional `-- note`, and stops at the deprecated SQLite part. Prose
 * lines are not indented that way and are skipped.
 */
export function parseSchemaViews(text: string): SchemaTable[] {
  const out: SchemaTable[] = []
  for (const l of text.split('\n')) {
    if (l.trim() === deprecated) break
    const m = l.match(line)
    if (!m) continue
    out.push({ name: m[1], columns: m[2].split(',').map((c) => c.trim()).filter(Boolean) })
  }
  return out
}

/** A queryable view or table and its columns, for the SQL editor's completion. */
export interface SchemaTable {
  name: string
  columns: string[]
}

const line = /^\s+([a-z_][a-z0-9_]*)\(([^)]*)\)/i

/**
 * Reads the views out of `GET /api/schema/views`, whose reference lists
 * each as an indented `name(col, col, …)` line followed by an optional
 * `-- note`. Prose lines are not indented that way and are skipped.
 */
export function parseSchemaViews(text: string): SchemaTable[] {
  const out: SchemaTable[] = []
  for (const l of text.split('\n')) {
    const m = l.match(line)
    if (!m) continue
    out.push({ name: m[1], columns: m[2].split(',').map((c) => c.trim()).filter(Boolean) })
  }
  return out
}

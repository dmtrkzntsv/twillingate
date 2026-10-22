import type { Contract } from '@/components/widgets/types'

/** How the editor draws one prop of a component's contract. */
export type PropField =
  | { key: string; kind: 'enum'; options: string[] }
  | { key: string; kind: 'boolean' }
  | { key: string; kind: 'enum-map'; options: string[] }
  | { key: string; kind: 'json' }

type Schema = Record<string, unknown>

const enumOf = (s: Schema | undefined): string[] | null =>
  Array.isArray(s?.enum) && s.enum.every((v) => typeof v === 'string') ? (s.enum as string[]) : null

/** One form field per prop, in the schema's order (spec D11). */
export function fieldsOf(schema: Schema): PropField[] {
  const props = (schema.properties ?? {}) as Record<string, Schema>
  return Object.entries(props).map(([key, s]): PropField => {
    const options = enumOf(s)
    if (options) return { key, kind: 'enum', options }
    if (s.type === 'boolean') return { key, kind: 'boolean' }
    const values = enumOf(s.additionalProperties as Schema | undefined)
    if (s.type === 'object' && values) return { key, kind: 'enum-map', options: values }
    return { key, kind: 'json' }
  })
}

function fits(field: PropField, v: unknown): boolean {
  switch (field.kind) {
    case 'enum':
      return typeof v === 'string' && field.options.includes(v)
    case 'boolean':
      return typeof v === 'boolean'
    case 'enum-map':
      return (
        typeof v === 'object' &&
        v !== null &&
        !Array.isArray(v) &&
        Object.values(v).every((x) => typeof x === 'string' && field.options.includes(x))
      )
    case 'json':
      return true
  }
}

/**
 * The props a component switch keeps: those whose key and value the new
 * schema accepts (spec D10). A `json` field keeps any value; the server
 * refuses a wrong shape on save.
 */
export function carryProps(props: Record<string, unknown>, schema: Schema): Record<string, unknown> {
  const byKey = new Map(fieldsOf(schema).map((f) => [f.key, f]))
  return Object.fromEntries(
    Object.entries(props).filter(([k, v]) => {
      const f = byKey.get(k)
      return f !== undefined && fits(f, v)
    }),
  )
}

/** One line on what the query must return, from the contract's inputs. */
export function needsHint(c: Contract): string {
  if (c.accepts.includes('md')) return 'markdown text'
  if (c.inputs.open) return 'any columns'
  const col = (x: { name: string; types: string[] }) => `${x.name} (${x.types.join(' or ')})`
  const req = c.inputs.columns.filter((x) => !x.optional).map(col)
  const opt = c.inputs.columns.filter((x) => x.optional).map(col)
  return `needs ${req.join(', ')}${opt.length ? `; optional ${opt.join(', ')}` : ''}`
}

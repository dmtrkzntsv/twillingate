import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { toRecords } from '@/lib/records'
import { widgets } from './index'
import type { Contract, Example, SqlData } from './types'

type Schema = { type?: string; enum?: unknown[]; items?: Schema; additionalProperties?: Schema | boolean }

/** The props schema's own vocabulary (types, enums, nested maps/arrays), no more: the Go side runs full JSON Schema. */
function propErrors(props: Record<string, unknown>, contract: Contract): string[] {
  const properties = (contract.props as { properties?: Record<string, Schema> }).properties ?? {}
  return Object.entries(props).flatMap(([key, value]) => {
    const schema = properties[key]
    return schema ? valueErrors(key, value, schema) : [`unknown prop ${key}`]
  })
}

function valueErrors(path: string, value: unknown, schema: Schema): string[] {
  if (schema.enum && !schema.enum.includes(value)) return [`${path}: ${JSON.stringify(value)} not in ${JSON.stringify(schema.enum)}`]
  switch (schema.type) {
    case 'boolean':
      return typeof value === 'boolean' ? [] : [`${path}: not a boolean`]
    case 'string':
      return typeof value === 'string' ? [] : [`${path}: not a string`]
    case 'array':
      return Array.isArray(value) ? value.flatMap((v, i) => valueErrors(`${path}[${i}]`, v, schema.items ?? {})) : [`${path}: not an array`]
    case 'object':
      if (typeof value !== 'object' || value === null || Array.isArray(value)) return [`${path}: not an object`]
      return typeof schema.additionalProperties === 'object'
        ? Object.entries(value).flatMap(([k, v]) => valueErrors(`${path}.${k}`, v, schema.additionalProperties as Schema))
        : []
    default:
      return []
  }
}

/** Columns the contract does not read, required ones missing, or a number column that does not parse. */
function dataErrors(data: SqlData, contract: Contract): string[] {
  const errors: string[] = []
  if (data.rows.length === 0) errors.push('no rows')
  if (!contract.inputs.open) {
    const declared = new Map(contract.inputs.columns.map((c) => [c.name, c]))
    for (const name of data.columns) if (!declared.has(name)) errors.push(`column ${name} is not an input`)
    for (const c of contract.inputs.columns) if (!c.optional && !data.columns.includes(c.name)) errors.push(`missing column ${c.name}`)
  }
  for (const row of data.rows) if (row.length !== data.columns.length) errors.push(`row ${JSON.stringify(row)} has the wrong width`)
  const numbers = contract.inputs.columns.filter((c) => c.types.length === 1 && c.types[0] === 'number').map((c) => c.name)
  for (const record of toRecords(data, contract))
    for (const name of numbers) if (name in record && Number.isNaN(record[name])) errors.push(`${name} is not a number`)
  const days = contract.inputs.columns.filter((c) => c.types.length === 1 && c.types[0] === 'day').map((c) => c.name)
  for (const record of toRecords(data, contract))
    for (const name of days) if (name in record && !/^\d{4}-\d{2}-\d{2}$/.test(String(record[name]))) errors.push(`${name} is not YYYY-MM-DD`)
  return errors
}

const entries = Object.entries(widgets)

describe('examples', () => {
  it.each(entries)('%s has at least one', (_name, module) => {
    expect(module.examples.length).toBeGreaterThan(0)
  })

  it.each(entries)('%s: every example fits the contract', (_name, module) => {
    for (const example of module.examples as Example[]) {
      const { contract } = module
      const errors = propErrors(example.props, contract)
      if ('markdown' in example.data) {
        if (!contract.accepts.includes('md')) errors.push('markdown data for an sql component')
      } else {
        if (!contract.accepts.includes('sql')) errors.push('sql data for an md component')
        errors.push(...dataErrors(example.data, contract))
      }
      expect(errors, `${example.title}`).toEqual([])
    }
  })

  it.each(entries)('%s: every example renders', (_name, module) => {
    const Component = module.default
    for (const example of module.examples) {
      const { container, unmount } = render(
        <div style={{ width: 600, height: 400 }}>
          <Component data={example.data} props={example.props} />
        </div>
      )
      expect(container.firstElementChild?.childElementCount, example.title).toBeGreaterThan(0)
      unmount()
    }
  })
})

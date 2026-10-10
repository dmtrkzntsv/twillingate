import { describe, expect, it } from 'vitest'
import { widgets } from '@/components/widgets'
import { carryProps, fieldsOf, needsHint } from './props-form'

describe('fieldsOf', () => {
  it('turns every component contract into fields', () => {
    for (const [name, mod] of Object.entries(widgets)) {
      const fields = fieldsOf(mod.contract.props)
      const keys = Object.keys((mod.contract.props.properties as object | undefined) ?? {})
      expect(fields.map((f) => f.key), name).toEqual(keys)
    }
  })
  it('knows enums, booleans, enum maps and falls back to json', () => {
    expect(fieldsOf(widgets.table.contract.props)).toEqual([
      { key: 'formats', kind: 'enum-map', options: ['number', 'percent', 'duration'] },
      { key: 'colorscale', kind: 'json' },
      { key: 'mode', kind: 'enum', options: ['local', 'remote'] },
    ])
    expect(fieldsOf(widgets.pie.contract.props)).toContainEqual({ key: 'donut', kind: 'boolean' })
  })
})

describe('carryProps', () => {
  it('keeps what the new schema accepts and drops the rest', () => {
    const line = widgets.line.contract.props
    expect(carryProps({ format: 'percent', curve: 'step', donut: true }, widgets.pie.contract.props)).toEqual({ format: 'percent', donut: true })
    expect(carryProps({ format: 'percent', curve: 'step', donut: true }, line)).toEqual({ format: 'percent', curve: 'step' })
    expect(carryProps({ curve: 'bogus' }, line)).toEqual({})
    expect(carryProps({ formats: { a: 'number' } }, line)).toEqual({})
  })
})

describe('needsHint', () => {
  it('names required and optional columns', () => {
    expect(needsHint(widgets.line.contract)).toBe('needs x (day or text), y (number); optional series (text)')
    expect(needsHint(widgets.table.contract)).toBe('any columns')
    expect(needsHint(widgets.markdown.contract)).toBe('markdown text')
  })
})

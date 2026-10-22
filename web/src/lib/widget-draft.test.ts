import { describe, expect, it } from 'vitest'
import type { Widget } from './api'
import { widgets } from '@/components/widgets'
import { addBody, changedFields, codeTypesOf, draftOf, isDirty, paramsOf, queryTemplate, sourceOf, sourceTypeOf, usesParams, withCode, withComponent } from './widget-draft'

interface Q {
  kind: 'stat' | 'trend' | 'breakdown'
  table: string
  metrics: { op: string; as?: string; column?: string; of?: string }[]
  dimensions?: string[]
  series?: string
  compare?: string
  order?: string
  other?: boolean
}

// native.Query.Columns(), mirrored: the result's columns before rename.
function columnsOf(q: Q): string[] {
  const name = (m: Q['metrics'][number]) => m.as || (m.column ? `${m.op}_${m.column}` : m.of ? `${m.op}_${m.of}` : m.op)
  const cols: string[] = []
  if (q.kind === 'trend') cols.push('day', ...(q.series ? ['series'] : []))
  if (q.kind === 'breakdown') cols.push(...(q.dimensions ?? []))
  cols.push(...q.metrics.map(name))
  if (q.kind === 'stat' && q.compare === 'previous') cols.push(...q.metrics.map((m) => `previous_${name(m)}`))
  return cols
}

// native.Query.ColumnTypes(), mirrored: each column's type, in order.
function typesOf(q: Q): string[] {
  const numeric = ['project_id', 'display_width', 'display_height', 'consent', 'bucket']
  const dim = (d: string) => (q.other ? 'text' : d === 'day' ? 'day' : numeric.includes(d) ? 'number' : 'text')
  const types: string[] = []
  if (q.kind === 'trend') types.push('day', ...(q.series ? ['text'] : []))
  if (q.kind === 'breakdown') types.push(...(q.dimensions ?? []).map(dim))
  types.push(...q.metrics.map(() => 'number'))
  if (q.kind === 'stat' && q.compare === 'previous') types.push(...q.metrics.map(() => 'number'))
  return types
}

const line: Widget = {
  widget_id: 7, dashboard_id: 3, name: 'visits', component: 'line', title: 'Visits', width: 6, height: 8,
  props: { curve: 'step' }, source: { type: 'sql', content: 'SELECT day AS x, views AS y FROM v' },
  follows_project: false, follows_range: false,
}

describe('draft', () => {
  it('starts empty for a new widget and from the widget for an edit', () => {
    expect(draftOf()).toEqual({ title: '', component: null, props: {}, code: 'sql', sql: '', query: '', markdown: '' })
    expect(draftOf(line)).toMatchObject({ title: 'Visits', component: 'line', sql: line.source.content, markdown: '' })
  })
  it('sends only what changed', () => {
    const d = draftOf(line)
    expect(changedFields(line, d)).toEqual({})
    expect(isDirty(line, d)).toBe(false)
    expect(changedFields(line, { ...d, title: 'Daily visits' })).toEqual({ title: 'Daily visits' })
    expect(changedFields(line, { ...d, props: { curve: 'linear' } })).toEqual({ props: { curve: 'linear' } })
    expect(changedFields(line, { ...d, component: 'area', sql: 'SELECT 1' })).toEqual({
      component: 'area', source: { type: 'sql', content: 'SELECT 1' },
    })
  })
  it('compares props whatever their key order', () => {
    const w = { ...line, props: { curve: 'step', format: 'percent', formats: { a: 'number', b: 'percent' } } }
    const d = { ...draftOf(w), props: { formats: { b: 'percent', a: 'number' }, format: 'percent', curve: 'step' } }
    expect(changedFields(w, d)).toEqual({})
    expect(isDirty(w, d)).toBe(false)
  })
  it('keeps sql and markdown apart, picking by the component', () => {
    const d = { ...draftOf(line), component: 'markdown', markdown: '# Hi' }
    expect(sourceOf(d)).toEqual({ type: 'md', content: '# Hi' })
    expect(sourceOf({ ...d, component: 'line' })).toEqual({ type: 'sql', content: line.source.content })
  })
  it('a removed component counts as a change once one is picked', () => {
    const removed = { ...line, component: null }
    expect(changedFields(removed, { ...draftOf(removed), component: 'line' })).toMatchObject({ component: 'line' })
  })
  it('builds an add body', () => {
    expect(addBody({ ...draftOf(), title: ' A ', component: 'stat', sql: 'SELECT 1 AS value' })).toEqual({
      component: 'stat', title: 'A', props: {}, source: { type: 'sql', content: 'SELECT 1 AS value' },
    })
  })
})

const statQuery: Widget = {
  ...line, component: 'stat', title: 'Views', props: {},
  source: { type: 'query', content: '{"query":{"kind":"stat","table":"views","metrics":[{"op":"count","as":"value"}]}}' },
  follows_project: true, follows_range: true,
}

describe('query sources', () => {
  it('opens a query widget on its query, not as sql', () => {
    const d = draftOf(statQuery)
    expect(d).toMatchObject({ code: 'query', query: statQuery.source.content, sql: '' })
    expect(sourceTypeOf(d)).toBe('query')
    expect(sourceOf(d)).toEqual(statQuery.source)
    expect(changedFields(statQuery, d)).toEqual({})
  })
  it('a new widget starts on sql', () => {
    expect(sourceTypeOf({ ...draftOf(), component: 'stat' })).toBe('sql')
  })
  it('switching keeps each type\'s content and sends the chosen one', () => {
    const d = { ...draftOf(line), sql: 'SELECT 1' }
    const q = withCode(d, 'query')
    expect(q.sql).toBe('SELECT 1')
    expect(q.query).toBe(queryTemplate('line'))
    expect(sourceOf(q)).toEqual({ type: 'query', content: queryTemplate('line') })
    const edited = { ...q, query: '{"query":{}}' }
    const back = withCode(edited, 'sql')
    expect(sourceOf(back)).toEqual({ type: 'sql', content: 'SELECT 1' })
    expect(withCode(back, 'query').query).toBe('{"query":{}}')
    expect(changedFields(line, edited)).toEqual({ source: { type: 'query', content: '{"query":{}}' } })
  })
  it('a remote table takes sql only', () => {
    expect(codeTypesOf('table', {})).toEqual(['sql', 'query'])
    expect(codeTypesOf('table', { mode: 'remote' })).toEqual(['sql'])
    expect(codeTypesOf('markdown', {})).toEqual([])
    const d = { ...draftOf(), component: 'table', props: { mode: 'remote' }, code: 'query' as const, sql: 'SELECT 1', query: '{}' }
    expect(sourceOf(d)).toEqual({ type: 'sql', content: 'SELECT 1' })
  })
  it('a query follows the project and the range', () => {
    expect(paramsOf({ ...draftOf(statQuery) })).toEqual({ project: true, range: true })
    expect(paramsOf({ ...draftOf(line) })).toEqual({ project: false, range: false })
    expect(paramsOf({ ...draftOf(), component: 'markdown', markdown: ':project' })).toEqual({ project: false, range: false })
  })
  it('starts each component on a query renamed to its inputs', () => {
    const t = (c: string) => JSON.parse(queryTemplate(c))
    expect(t('stat')).toEqual({ query: { kind: 'stat', table: 'views', metrics: [{ op: 'count' }] }, rename: { count: 'value' } })
    expect(t('line')).toMatchObject({ query: { kind: 'trend' }, rename: { day: 'x', count: 'y' } })
    expect(t('bar')).toMatchObject({ query: { kind: 'trend' }, rename: { day: 'x', count: 'y' } })
    expect(t('pie')).toMatchObject({ query: { kind: 'breakdown', dimensions: ['path'] }, rename: { path: 'label', count: 'value' } })
    expect(t('bar_list').rename).toEqual({ path: 'label', count: 'value' })
    expect(t('table')).toEqual({ query: { kind: 'breakdown', table: 'views', metrics: [{ op: 'count' }], dimensions: ['path'], limit: 10 } })
  })
})

describe('query templates', () => {
  const withQuery = Object.entries(widgets).filter(([, m]) => m.contract.accepts.includes('query'))
  it.each(withQuery.map(([name]) => name))('%s starts on a query whose renamed columns fit its inputs', (name) => {
    const c = widgets[name].contract
    const { query, rename = {} } = JSON.parse(queryTemplate(name)) as { query: Q; rename?: Record<string, string> }
    const cols = columnsOf(query)
    for (const k of Object.keys(rename)) expect(cols).toContain(k)
    expect(new Set(Object.values(rename)).size).toBe(Object.values(rename).length)
    const out = cols.map((col) => rename[col] ?? col)
    if (query.kind === 'trend') expect(query.metrics).toHaveLength(1)
    if (query.kind === 'breakdown') expect(query.dimensions?.length).toBeGreaterThan(0)
    if (c.inputs.open) return
    for (const input of c.inputs.columns.filter((x) => !x.optional)) expect(out).toContain(input.name)
    const declared = c.inputs.columns.map((x) => x.name)
    for (const col of out) expect(declared).toContain(col)
    // The types the server checks at save: text takes any column.
    const types = typesOf(query)
    out.forEach((col, i) => {
      const want = c.inputs.columns.find((x) => x.name === col)!.types
      if (!want.includes('text')) expect(want).toContain(types[i])
    })
    // A breakdown's order names a metric or a dimension.
    if (query.order) expect(cols).toContain(query.order)
  })
  it('orders the combo by day and the heatmap by count', () => {
    expect(JSON.parse(queryTemplate('combo')).query).toMatchObject({ dimensions: ['day'], order: 'day', limit: 1000 })
    expect(JSON.parse(queryTemplate('heatmap')).query).toMatchObject({ dimensions: ['device', 'platform'], order: 'count' })
  })
  it('moves an untouched template to the new component, and keeps an edited query', () => {
    const d = withCode({ ...draftOf(), component: 'stat' }, 'query')
    const moved = withComponent(d, 'line', {})
    expect(moved).toMatchObject({ component: 'line', query: queryTemplate('line') })
    const edited = { ...d, query: '{"query":{}}' }
    expect(withComponent(edited, 'line', { curve: 'step' })).toMatchObject({ component: 'line', props: { curve: 'step' }, query: '{"query":{}}' })
  })
  it('an untouched template on a new widget is no change once back on sql', () => {
    const d = withCode({ ...draftOf(), component: 'stat' }, 'query')
    expect(isDirty(undefined, withCode(d, 'sql'))).toBe(isDirty(undefined, { ...draftOf(), component: 'stat' }))
    expect(isDirty(undefined, { ...draftOf(), sql: '' , query: queryTemplate(null) })).toBe(false)
    expect(isDirty(undefined, { ...withCode(d, 'sql'), query: '{}' })).toBe(true)
  })
})

describe('usesParams', () => {
  it('finds the parameters the sql uses', () => {
    expect(usesParams('SELECT * FROM v WHERE project_id=:project AND day BETWEEN :from AND :to')).toEqual({ project: true, range: true })
    expect(usesParams('SELECT :to')).toEqual({ project: false, range: true })
  })
  it('ignores them in strings, quoted names and comments', () => {
    expect(usesParams("SELECT ':project' AS x, \":from\" -- :to\n/* :project */ FROM v")).toEqual({ project: false, range: false })
  })
  it('finds one beside a string that names it', () => {
    expect(usesParams("SELECT ':project', :project")).toEqual({ project: true, range: false })
  })
  it('reads past an escaped quote to the end of the string', () => {
    expect(usesParams("SELECT 'it''s :project'")).toEqual({ project: false, range: false })
  })
  it('does not match a longer name', () => {
    expect(usesParams('SELECT :projects, :fromage')).toEqual({ project: false, range: false })
  })
})

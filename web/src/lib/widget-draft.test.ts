import { describe, expect, it } from 'vitest'
import type { Widget } from './api'
import { addBody, changedFields, codeTypesOf, draftOf, isDirty, paramsOf, queryTemplate, sourceOf, sourceTypeOf, usesParams, withCode } from './widget-draft'

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

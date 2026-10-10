import { describe, expect, it } from 'vitest'
import type { Widget } from './api'
import { addBody, changedFields, draftOf, isDirty, sourceOf, usesParams } from './widget-draft'

const line: Widget = {
  widget_id: 7, dashboard_id: 3, name: 'visits', component: 'line', title: 'Visits', width: 6, height: 8,
  props: { curve: 'step' }, source: { type: 'sql', content: 'SELECT day AS x, views AS y FROM v' },
  follows_project: false, follows_range: false,
}

describe('draft', () => {
  it('starts empty for a new widget and from the widget for an edit', () => {
    expect(draftOf()).toEqual({ title: '', component: null, props: {}, sql: '', markdown: '' })
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
    expect(addBody({ title: ' A ', component: 'stat', props: {}, sql: 'SELECT 1 AS value', markdown: '' })).toEqual({
      component: 'stat', title: 'A', props: {}, source: { type: 'sql', content: 'SELECT 1 AS value' },
    })
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
  it('does not match a longer name', () => {
    expect(usesParams('SELECT :projects, :fromage')).toEqual({ project: false, range: false })
  })
})

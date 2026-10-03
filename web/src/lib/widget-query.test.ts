import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import type { Widget, WidgetDataQuery } from './api'
import { emptyView, type TableView } from './table-view'
import { isRemoteTable, shownViews, viewQuery, widgetQuery } from './widget-query'

function widget(component: string | null, props: Record<string, unknown> = {}): Widget {
  return {
    widget_id: 42,
    dashboard_id: 1,
    name: 'attrs',
    component,
    width: 6,
    height: 10,
    props,
    source: { type: 'sql', content: 'SELECT 1' },
    follows_project: true,
    follows_range: true,
  }
}

const remote = widget('table', { mode: 'remote' })
const columns = ['Attribute', 'Count']
const view: TableView = {
  filters: [
    { column: 'Attribute', op: 'in', value: ['plan'] },
    { column: 'Platform', op: '=', value: 'web' },
  ],
  sort: { column: 'Count', dir: 'desc' },
  offset: 1000,
}

describe('viewQuery', () => {
  it('knows a remote table from a local one and from other components', () => {
    expect(isRemoteTable(remote)).toBe(true)
    expect(isRemoteTable(widget('table'))).toBe(false)
    expect(isRemoteTable(widget('table', { mode: 'local' }))).toBe(false)
    expect(isRemoteTable(widget('stat', { mode: 'remote' }))).toBe(false)
  })

  it('adds nothing for a widget that is not a remote table', () => {
    expect(viewQuery(widget('table'), view, columns)).toEqual({})
  })

  it('sends only what the answer can apply, and omits what is at its default', () => {
    expect(viewQuery(remote, view, columns)).toEqual({
      filters: JSON.stringify([view.filters[0]]),
      sort: 'Count:desc',
      offset: 1000,
    })
    expect(viewQuery(remote, emptyView, columns)).toEqual({})
    expect(viewQuery(remote, { ...view, sort: { column: 'Gone', dir: 'asc' } }, columns)).not.toHaveProperty('sort')
  })

  it('sends the stored view whole before an answer has named the columns', () => {
    expect(viewQuery(remote, view, undefined)).toEqual({
      filters: JSON.stringify(view.filters),
      sort: 'Count:desc',
      offset: 1000,
    })
  })

  it('keys a remote table by its view, so each page is cached on its own', () => {
    const params = { project_id: 7, from: '2026-09-20', to: '2026-09-26' }
    const first = widgetQuery(remote, params).queryKey
    const paged = widgetQuery(remote, params, false, viewQuery(remote, view, columns)).queryKey
    expect(paged).not.toEqual(first)
    expect(paged.slice(0, 5)).toEqual(first.slice(0, 5))
  })
})

describe('shownViews', () => {
  const params = { project_id: 7, from: '2026-09-20', to: '2026-09-26' }

  function watch(client: QueryClient, view: Partial<WidgetDataQuery>, enabled = true) {
    const options = { ...widgetQuery(remote, params, false, view), queryFn: () => new Promise<never>(() => {}), enabled }
    return new QueryObserver(client, options).subscribe(() => {})
  }

  it('reads back the view of each remote page on screen, so a refresh from outside the card asks for it', () => {
    const client = new QueryClient()
    const shown = viewQuery(remote, view, columns)
    const stops = [watch(client, shown), watch(client, {}, false)]
    expect(shownViews(client, remote, params)).toEqual([shown])
    stops.forEach((stop) => stop())
  })

  it('falls back to no view when nothing is on screen', () => {
    expect(shownViews(new QueryClient(), widget('stat'), params)).toEqual([{}])
  })
})

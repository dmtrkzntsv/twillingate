import { describe, expect, it } from 'vitest'
import type { DashboardDetail, Widget } from './api'
import { selectionParams, viewBody, widgetParams, withSavedView } from './view'

const widget = (follows_project: boolean, follows_range: boolean) => ({ follows_project, follows_range }) as Widget

describe('view helpers', () => {
  it('puts a preset selection in the URL without dates', () => {
    expect(selectionParams({ projectId: 7, range: '7d' }).toString()).toBe('project=7&range=7d')
  })

  it('puts a custom selection in the URL with its dates', () => {
    expect(selectionParams({ range: 'custom', from: '2026-08-01', to: '2026-08-31' }).toString()).toBe(
      'range=custom&from=2026-08-01&to=2026-08-31'
    )
  })

  it('saves only the project, and only on a dashboard with a project switcher', () => {
    const sel = { projectId: 7, range: 'custom' as const, from: '2026-08-01', to: '2026-08-31' }
    expect(viewBody(sel, { project: true, range: true })).toEqual({ project_id: 7 })
    expect(viewBody(sel, { project: false, range: true })).toBeNull()
    expect(viewBody({ range: '7d' }, { project: true, range: false })).toBeNull()
  })

  it('asks a widget only for what it follows', () => {
    const range = { from: '2026-09-20', to: '2026-09-26' }
    expect(widgetParams(widget(true, true), { projectId: 7 }, range)).toEqual({ project_id: 7, ...range })
    expect(widgetParams(widget(false, true), { projectId: 7 }, range)).toEqual(range)
    expect(widgetParams(widget(true, false), { projectId: 7 }, range)).toEqual({ project_id: 7 })
    expect(widgetParams(widget(false, false), { projectId: 7 }, range)).toEqual({})
    const follower = { ...widget(false, false), follows_selectors: ['env'] }
    const picked = { selection: { env: ['prod'] }, order: ['env'], columns: { env: 'a' } }
    expect(widgetParams(follower, { projectId: 7 }, range, picked)).toEqual({
      selectors: { env: ['prod'] },
      selector_columns: { env: 'a' },
    })
    expect(widgetParams(follower, { projectId: 7 }, range, { ...picked, selection: {} })).toEqual({})
  })

  it('applies a saved view to a cached dashboard', () => {
    const d = { dashboard_id: 1, project_id: 7 } as DashboardDetail
    expect(withSavedView(d, { project_id: 1 })).toMatchObject({ dashboard_id: 1, project_id: 1 })
  })
})

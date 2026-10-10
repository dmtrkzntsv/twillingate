import { afterEach, describe, expect, it, vi } from 'vitest'
import { QueryClient } from '@tanstack/react-query'
import { endpoints, type DashboardDetail, type Widget } from './api'
import { dashboardQuery } from './queries'

function widget(id: number, over: Partial<Widget> = {}): Widget {
  return {
    widget_id: id,
    dashboard_id: 7,
    name: `w-${id}`,
    component: 'line',
    title: `W${id}`,
    width: 6,
    height: 4,
    props: { x: 'day' },
    source: { type: 'sql', content: `SELECT ${id}` },
    follows_project: true,
    follows_range: true,
    ...over,
  }
}

function detail(widgets: Widget[]): DashboardDetail {
  return {
    dashboard_id: 7,
    title: 'Mine',
    owner: 'user',
    group_id: 7,
    range: '7d',
    sidebar: true,
    project_tab: false,
    follows_project: true,
    follows_range: true,
    widgets,
    tabs: [{ dashboard_id: 7, title: 'Mine' }],
  }
}

// A refetch parses a fresh copy of the JSON: nothing in it is the cached object.
const fresh = <T>(value: T): T => structuredClone(value)

const share = dashboardQuery(7).structuralSharing

afterEach(() => {
  vi.restoreAllMocks()
})

describe('dashboardQuery', () => {
  it('keeps each unchanged widget by id when the widgets are reordered', () => {
    const old = detail([widget(1), widget(2), widget(3), widget(4)])
    // The first moved to third, and the fourth retitled.
    const next = fresh(detail([widget(2), widget(3), widget(1), widget(4, { title: 'Renamed' })]))

    const shared = share(old, next) as DashboardDetail

    expect(shared).not.toBe(old)
    expect(shared.widgets.map((w) => w.widget_id)).toEqual([2, 3, 1, 4])
    expect(shared.widgets[0]).toBe(old.widgets[1])
    expect(shared.widgets[1]).toBe(old.widgets[2])
    expect(shared.widgets[2]).toBe(old.widgets[0])
    expect(shared.widgets[3]).not.toBe(old.widgets[3])
    expect(shared.widgets[3]).toEqual(widget(4, { title: 'Renamed' }))
    // The rest of the detail is shared as TanStack shares it.
    expect(shared.tabs).toBe(old.tabs)
  })

  it('returns the cached detail itself when nothing changed', () => {
    const old = detail([widget(1), widget(2)])
    expect(share(old, fresh(old))).toBe(old)
  })

  it('takes the first answer as it is', () => {
    const next = detail([widget(1)])
    expect(share(undefined, next)).toBe(next)
  })

  it('keeps the unchanged widgets across a refetch through the query client', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const first = detail([widget(1), widget(2), widget(3)])
    const spy = vi.spyOn(endpoints, 'dashboard').mockResolvedValue(fresh(first))
    // fetchQuery resolves with what the fetch returned; the cache, which the
    // page reads, holds what was shared.
    const cached = () => client.getQueryData<DashboardDetail>(dashboardQuery(7).queryKey)!
    await client.fetchQuery(dashboardQuery(7))
    const before = cached()

    spy.mockResolvedValue(fresh(detail([widget(3), widget(1), widget(2)])))
    await client.fetchQuery(dashboardQuery(7))
    const after = cached()

    expect(after.widgets.map((w) => w.widget_id)).toEqual([3, 1, 2])
    expect(after.widgets[0]).toBe(before.widgets[2])
    expect(after.widgets[1]).toBe(before.widgets[0])
    expect(after.widgets[2]).toBe(before.widgets[1])
  })
})

import { describe, expect, it } from 'vitest'
import type { ProjectTab } from './api'
import { FORMS_ID, pickerSections, rangeParams, SETUP_ID, tabPath, userAfter } from './project-tabs'
import { dashboardsList } from '@/test/fixtures'

const tabs: ProjectTab[] = [
  { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1 },
  { dashboard_id: 2, title: 'Product', owner: 'system', group_id: 1 },
  { dashboard_id: 13, title: 'Marketing', owner: 'user', group_id: 13 },
  { dashboard_id: 10, title: 'Launch week', owner: 'user', group_id: 10 },
  { dashboard_id: 14, title: 'Funnel', owner: 'user', group_id: 13 },
]

describe('tabPath', () => {
  it('opens Setup at /setup and a dashboard under /dashboards', () => {
    expect(tabPath(7, SETUP_ID)).toBe('/projects/7/setup')
    expect(tabPath(7, 13)).toBe('/projects/7/dashboards/13')
  })

  it('opens Forms at /forms, an id no dashboard or Setup has', () => {
    expect(FORMS_ID).not.toBe(SETUP_ID)
    expect(FORMS_ID).toBeLessThan(1)
    expect(tabPath(7, FORMS_ID)).toBe('/projects/7/forms')
    expect(tabPath(7, FORMS_ID, 'range=30d')).toBe('/projects/7/forms?range=30d')
  })

  it('appends a search when there is one', () => {
    expect(tabPath(7, SETUP_ID, 'range=30d')).toBe('/projects/7/setup?range=30d')
    expect(tabPath(7, 13, 'range=custom&from=2026-09-01&to=2026-09-10')).toBe(
      '/projects/7/dashboards/13?range=custom&from=2026-09-01&to=2026-09-10'
    )
    expect(tabPath(7, 13, '')).toBe('/projects/7/dashboards/13')
  })
})

describe('rangeParams', () => {
  it('keeps range, from and to, and drops the rest', () => {
    const url = new URLSearchParams('project=4&range=custom&from=2026-09-01&to=2026-09-10&x=1')
    expect(rangeParams(url).toString()).toBe('range=custom&from=2026-09-01&to=2026-09-10')
    expect(rangeParams(new URLSearchParams('project=4')).toString()).toBe('')
  })
})

describe('pickerSections', () => {
  it('offers the built-ins and live dashboards of the user that are not tabs yet, in list order', () => {
    const list = dashboardsList().dashboards
    list[2] = { ...list[2], sidebar: false } // Users, hidden from the sidebar
    const { builtin, own } = pickerSections(list, tabs)
    expect(builtin.map((d) => d.title)).toEqual(['Users', 'Groups', 'Retention'])
    // Old experiment is archived; Launch week, Marketing and Funnel are tabs already.
    expect(own.map((d) => d.title)).toEqual([])
    const { own: more } = pickerSections(list, tabs.slice(0, 3))
    expect(more.map((d) => d.title)).toEqual(['Launch week', 'Funnel'])
  })
})

describe('userAfter', () => {
  it('gives 0 for the first place among the user tabs', () => {
    expect(userAfter(tabs, 14, 0)).toBe(0)
  })

  it('gives the user tab before the new place once the moving one is taken out', () => {
    // Marketing, Launch week, Funnel: Marketing to the end goes after Funnel.
    expect(userAfter(tabs, 13, 2)).toBe(14)
    // Funnel to the middle goes after Marketing.
    expect(userAfter(tabs, 14, 1)).toBe(13)
    // Marketing one right goes after Launch week.
    expect(userAfter(tabs, 13, 1)).toBe(10)
  })
})

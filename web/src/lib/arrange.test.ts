import { describe, expect, it } from 'vitest'
import type { DashboardInfo, DashboardTab } from './api'
import { liveGroups, moveGroupBody, moveTabBody, nextAfterArchive, purgeDate, reorder } from './arrange'

function mkInfo(dashboard_id: number, title: string, owner: 'system' | 'user', group_id: number, extra: Partial<DashboardInfo> = {}): DashboardInfo {
  return { dashboard_id, title, owner, group_id, widgets: 1, ...extra }
}

function tab(dashboard_id: number, title: string): DashboardTab {
  return { dashboard_id, title }
}

describe('liveGroups', () => {
  it('skips archived rows and keeps list order', () => {
    const list = [
      mkInfo(1, 'Views', 'system', 1),
      mkInfo(2, 'Product', 'system', 1),
      mkInfo(10, 'Launch week', 'user', 10),
      mkInfo(11, 'Old experiment', 'user', 11, { archived_at: '2026-09-01T00:00:00Z' }),
      mkInfo(13, 'Marketing', 'user', 13),
      mkInfo(14, 'Funnel', 'user', 13),
    ]

    expect(liveGroups(list)).toEqual([
      { groupId: 1, owner: 'system', members: [list[0], list[1]] },
      { groupId: 10, owner: 'user', members: [list[2]] },
      { groupId: 13, owner: 'user', members: [list[4], list[5]] },
    ])
  })

  it('drops a group entirely when every member is archived', () => {
    const list = [mkInfo(1, 'Views', 'system', 1), mkInfo(11, 'Gone', 'user', 11, { archived_at: '2026-09-01T00:00:00Z' })]

    expect(liveGroups(list)).toEqual([{ groupId: 1, owner: 'system', members: [list[0]] }])
  })
})

describe('moveGroupBody', () => {
  // Three user groups in sidebar order: 10, 20 (whose literal first
  // dashboard, 20, is archived — its live leader is 21), 30.
  const list = [
    mkInfo(10, 'Launch week', 'user', 10),
    mkInfo(20, 'Old lead', 'user', 20, { archived_at: '2026-09-01T00:00:00Z' }),
    mkInfo(21, 'New lead', 'user', 20),
    mkInfo(30, 'Marketing', 'user', 30),
  ]
  const groups = liveGroups(list)

  it('moves to the top with {after: 0}', () => {
    expect(moveGroupBody(groups, 30, 0)).toEqual({ after: 0 })
  })

  it('moving down one names the live leader of the group now before it, skipping an archived first member', () => {
    expect(moveGroupBody(groups, 10, 1)).toEqual({ after: 21 })
  })

  it('returns null when the group is already at that index', () => {
    expect(moveGroupBody(groups, 10, 0)).toBeNull()
    expect(moveGroupBody(groups, 30, 2)).toBeNull()
  })

  it('only ever considers user groups, never a system one', () => {
    const withSystem = liveGroups([mkInfo(1, 'Views', 'system', 1), ...list])
    expect(moveGroupBody(withSystem, 30, 0)).toEqual({ after: 0 })
  })
})

describe('moveTabBody', () => {
  const tabs = [tab(1, 'Views'), tab(2, 'Product'), tab(3, 'Users')]

  it('moving to first gives {group_id, after: 0}', () => {
    expect(moveTabBody(tabs, 2, 1, 0)).toEqual({ group_id: 1, after: 0 })
  })

  it('moving to the end names the last tab', () => {
    expect(moveTabBody(tabs, 1, 1, 2)).toEqual({ after: 3 })
  })

  it('returns null when the tab is already at that index', () => {
    expect(moveTabBody(tabs, 1, 1, 0)).toBeNull()
    expect(moveTabBody(tabs, 3, 1, 2)).toBeNull()
  })
})

describe('nextAfterArchive', () => {
  const tabs = [tab(1, 'Views'), tab(2, 'Product'), tab(3, 'Users')]

  it('goes to the next tab from the middle', () => {
    expect(nextAfterArchive(tabs, 2)).toBe('/dashboards/3')
  })

  it('goes to the previous tab from the last', () => {
    expect(nextAfterArchive(tabs, 3)).toBe('/dashboards/2')
  })

  it('goes to "/" for a lone tab', () => {
    expect(nextAfterArchive([tab(5, 'Scratch')], 5)).toBe('/dashboards')
  })
})

describe('purgeDate', () => {
  it('is 30 days after the archive date', () => {
    expect(purgeDate('2026-09-01T00:00:00Z', 30)).toEqual(new Date('2026-10-01T00:00:00Z'))
  })

  it('is undefined when days is 0 or absent', () => {
    expect(purgeDate('2026-09-01T00:00:00Z', 0)).toBeUndefined()
    expect(purgeDate('2026-09-01T00:00:00Z')).toBeUndefined()
  })
})

describe('reorder', () => {
  it('puts the dragged id at the index of the one it was dropped on', () => {
    expect(reorder([10, 20, 30], 10, 30)).toEqual({ to: 2 })
    expect(reorder([10, 20, 30], 30, 10)).toEqual({ to: 0 })
    expect(reorder([10, 20, 30], 20, 30)).toEqual({ to: 2 })
  })

  it('is null when dropped on itself or on an id not in the list', () => {
    expect(reorder([10, 20, 30], 10, 10)).toBeNull()
    expect(reorder([10, 20, 30], 10, 99)).toBeNull()
    expect(reorder([10, 20, 30], 99, 10)).toBeNull()
  })
})

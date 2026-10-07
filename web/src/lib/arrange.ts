import type { DashboardInfo, DashboardTab, MoveBody } from './api'

/** One sidebar entry: a group's live members in order (tabs D4, D20). */
export interface Group {
  groupId: number
  owner: 'system' | 'user'
  members: DashboardInfo[]
}

/** Live dashboards grouped, in list order. */
export function liveGroups(list: DashboardInfo[]): Group[] {
  const groups: Group[] = []
  for (const d of list) {
    if (d.archived_at) continue
    const last = groups.at(-1)
    if (last && last.groupId === d.group_id) {
      last.members.push(d)
    } else {
      groups.push({ groupId: d.group_id, owner: d.owner, members: [d] })
    }
  }
  return groups
}

/**
 * A group's name (group names D2, D8): its stored `group_title`, which
 * every member carries, or else its first live member's title, as the
 * sidebar named every group before groups had names.
 */
export function groupName(members: DashboardInfo[]): string {
  const named = members.find((m) => m.group_title)
  if (named?.group_title) return named.group_title
  return (members.find((m) => !m.archived_at) ?? members[0]).title
}

/** True when a dashboard title or group name has fewer than 2 characters once trimmed (D5, D11). */
export function nameTooShort(s: string): boolean {
  return [...s.trim()].length < 2
}

/**
 * The update_dashboard body that puts `group` at index `to` among the user
 * groups (after removing it), or null if that is where it is. Names only
 * live ids: the anchor is the target group's first *live* member, which
 * `liveGroups` already guarantees `members[0]` to be even when that
 * group's literal first dashboard is archived (tabs D4; D15). An
 * out-of-range `to` also returns null, as if unchanged.
 */
export function moveGroupBody(groups: Group[], groupId: number, to: number): MoveBody | null {
  const userGroups = groups.filter((g) => g.owner === 'user')
  const from = userGroups.findIndex((g) => g.groupId === groupId)
  if (from === -1 || from === to) return null
  if (to === 0) return { after: 0 }
  const without = userGroups.filter((g) => g.groupId !== groupId)
  const target = without[to - 1]
  if (!target) return null
  return { after: target.members[0].dashboard_id }
}

/**
 * The body that puts tab `id` at index `to` among `tabs` (after removing
 * it): `{group_id: own, after: 0}` for first, else `{after: <tab
 * before>}`. Null if unchanged, or if `to` is out of range (tabs D6-D7; D15).
 */
export function moveTabBody(tabs: DashboardTab[], id: number, groupId: number, to: number): MoveBody | null {
  const from = tabs.findIndex((t) => t.dashboard_id === id)
  if (from === -1 || from === to) return null
  if (to === 0) return { group_id: groupId, after: 0 }
  const without = tabs.filter((t) => t.dashboard_id !== id)
  const before = without[to - 1]
  if (!before) return null
  return { after: before.dashboard_id }
}

/**
 * Where a drag ends: `activeId` dropped on `overId` takes `overId`'s index,
 * the `to` that `moveGroupBody` and `moveTabBody` read (the index after
 * removing the moved one). Null when dropped on itself or when either id
 * is not in `ids` (D14, D15).
 */
export function reorder(ids: number[], activeId: number, overId: number): { to: number } | null {
  const from = ids.indexOf(activeId)
  const to = ids.indexOf(overId)
  if (from === -1 || to === -1 || from === to) return null
  return { to }
}

/**
 * The `after` that moves `id` to index `to` of `ids` (the index after
 * removing it, as `reorder` gives it): 0 for first, else the id before
 * that place.
 */
export function afterAt(ids: number[], id: number, to: number): number {
  const rest = ids.filter((x) => x !== id)
  return to <= 0 ? 0 : (rest[Math.min(to, rest.length) - 1] ?? 0)
}

/** Where to go after archiving `id`: the next live tab, else the previous, else '/dashboards' (D12). */
export function nextAfterArchive(tabs: DashboardTab[], id: number): string {
  const idx = tabs.findIndex((t) => t.dashboard_id === id)
  if (idx === -1) return '/dashboards'
  const target = tabs[idx + 1] ?? tabs[idx - 1]
  return target ? `/dashboards/${target.dashboard_id}` : '/dashboards'
}

/** The date an archived dashboard is purged, or undefined when days is 0/absent (D17a). */
export function purgeDate(archivedAt: string, days?: number): Date | undefined {
  if (!days) return undefined
  const date = new Date(archivedAt)
  date.setUTCDate(date.getUTCDate() + days)
  return date
}

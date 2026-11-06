import type { DashboardInfo, ProjectTab } from './api'

/** Settings, behind the gear; no dashboard has id 0. */
export const SETTINGS_ID = 0

/** The Forms tab's id in `ReportTabs`: never a dashboard's (forms D12). */
export const FORMS_ID = -1

/**
 * A project's Settings tabs, in order (project settings tabs), each at
 * `/settings/:section`; plain `/settings` shows the first.
 */
export const SETTINGS_SECTIONS = [
  { id: 'usage', title: 'Usage' },
  { id: 'install', title: 'Install' },
  { id: 'domains', title: 'Domains' },
  { id: 'events', title: 'Events' },
  { id: 'attributes', title: 'Attributes' },
  { id: 'danger', title: 'Danger' },
] as const

export type SettingsSection = (typeof SETTINGS_SECTIONS)[number]['id']

/** The Settings tabs a project has: an archived one, which takes nothing and is not archived again, has no Install and no Danger. */
export function settingsSections(archived: boolean): (typeof SETTINGS_SECTIONS)[number][] {
  return SETTINGS_SECTIONS.filter((s) => !archived || (s.id !== 'install' && s.id !== 'danger'))
}

/** The Settings tab `param` names on a project, the first when it names none, or null when it is not one of the project's. */
export function settingsSection(archived: boolean, param: string | undefined): SettingsSection | null {
  if (param === undefined) return SETTINGS_SECTIONS[0].id
  return settingsSections(archived).find((s) => s.id === param)?.id ?? null
}

/** Where a Settings tab lives: `/settings/:section`, or `/settings` alone for the first. */
export function settingsPath(projectId: number, section?: SettingsSection, search = ''): string {
  const path = section ? `/projects/${projectId}/settings/${section}` : `/projects/${projectId}/settings`
  return search ? `${path}?${search}` : path
}

/** Where a project's tab lives: Settings at `/settings`, Forms at `/forms`, a dashboard under `/dashboards/:id` (D8). */
export function tabPath(projectId: number, dashboardId: number, search = ''): string {
  const path =
    dashboardId === SETTINGS_ID
      ? `/projects/${projectId}/settings`
      : dashboardId === FORMS_ID
        ? `/projects/${projectId}/forms`
        : `/projects/${projectId}/dashboards/${dashboardId}`
  return search ? `${path}?${search}` : path
}

/** The part of a project page's URL that every tab shares: the range (D8). */
export function rangeParams(url: URLSearchParams): URLSearchParams {
  const params = new URLSearchParams()
  for (const key of ['range', 'from', 'to']) {
    const v = url.get(key)
    if (v !== null) params.set(key, v)
  }
  return params
}

/**
 * What "+" offers: the built-ins not on the project (hidden from the
 * sidebar or not), and the user's live dashboards not on it, in list order.
 */
export function pickerSections(dashboards: DashboardInfo[], tabs: ProjectTab[]): { builtin: DashboardInfo[]; own: DashboardInfo[] } {
  const taken = new Set(tabs.map((t) => t.dashboard_id))
  const free = dashboards.filter((d) => !taken.has(d.dashboard_id))
  return {
    builtin: free.filter((d) => d.owner === 'system'),
    own: free.filter((d) => d.owner === 'user' && !d.archived_at),
  }
}

/**
 * The `after` that moves tab `id` to index `to` among all of a project's
 * tabs (one order, built-ins included: project landing D1): 0 for first,
 * else the tab before that place once `id` is taken out.
 */
export function tabAfter(tabs: ProjectTab[], id: number, to: number): number {
  const rest = tabs.filter((t) => t.dashboard_id !== id)
  return to <= 0 ? 0 : (rest[Math.min(to, rest.length) - 1]?.dashboard_id ?? 0)
}

/** Where opening project `projectId` lands (D3): the remembered tab while it is one, else the first, else Settings. */
export function landingPath(projectId: number, tabs: ProjectTab[], remembered: number | null, search: string): string {
  const id = tabs.some((t) => t.dashboard_id === remembered) ? remembered! : (tabs[0]?.dashboard_id ?? SETTINGS_ID)
  return tabPath(projectId, id, search)
}

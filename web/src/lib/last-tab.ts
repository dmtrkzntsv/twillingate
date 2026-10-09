/** Where each project's last dashboard tab is kept on this device (project landing D3). */
export const LAST_TAB_KEY = 'twillingate.project.last_tab'

function readAll(): Record<string, number> {
  try {
    const raw = localStorage.getItem(LAST_TAB_KEY)
    const v: unknown = raw === null ? null : JSON.parse(raw)
    if (v === null || typeof v !== 'object' || Array.isArray(v)) return {}
    const out: Record<string, number> = {}
    for (const [k, id] of Object.entries(v)) if (Number.isInteger(id) && (id as number) > 0) out[k] = id as number
    return out
  } catch {
    return {}
  }
}

/** The dashboard last opened on project `projectId` here, or null. */
export function readLastTab(projectId: number): number | null {
  return readAll()[String(projectId)] ?? null
}

/** Remembers `dashboardId` as project `projectId`'s last tab; storage full or blocked is ignored. */
export function writeLastTab(projectId: number, dashboardId: number): void {
  const all = readAll()
  if (all[String(projectId)] === dashboardId) return
  all[String(projectId)] = dashboardId
  try {
    localStorage.setItem(LAST_TAB_KEY, JSON.stringify(all))
  } catch {
    // The page still works; the next visit opens the first tab.
  }
}

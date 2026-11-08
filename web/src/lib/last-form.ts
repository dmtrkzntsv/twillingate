/** Where each project's last opened form is kept on this device, as `last-tab.ts` keeps its last dashboard. */
export const LAST_FORM_KEY = 'twillingate.project.last_form'

function readAll(): Record<string, string> {
  try {
    const raw = localStorage.getItem(LAST_FORM_KEY)
    const v: unknown = raw === null ? null : JSON.parse(raw)
    if (v === null || typeof v !== 'object' || Array.isArray(v)) return {}
    const out: Record<string, string> = {}
    for (const [k, name] of Object.entries(v)) if (typeof name === 'string' && name !== '') out[k] = name
    return out
  } catch {
    return {}
  }
}

/** The form last opened on project `projectId` here, or null. */
export function readLastForm(projectId: number): string | null {
  return readAll()[String(projectId)] ?? null
}

/** Remembers `name` as project `projectId`'s last form; storage full or blocked is ignored. */
export function writeLastForm(projectId: number, name: string): void {
  const all = readAll()
  if (all[String(projectId)] === name) return
  all[String(projectId)] = name
  try {
    localStorage.setItem(LAST_FORM_KEY, JSON.stringify(all))
  } catch {
    // The page still works; the next visit opens the first form.
  }
}

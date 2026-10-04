const KEY = 'twillingate.last_dashboard'

/** Remembers the dashboard opened last on this device, for "/dashboards" (D34). */
export function rememberDashboard(id: number): void {
  localStorage.setItem(KEY, String(id))
}

export function lastDashboard(): number | undefined {
  const id = Number(localStorage.getItem(KEY))
  return Number.isInteger(id) && id > 0 ? id : undefined
}

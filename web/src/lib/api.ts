import { getAuthHeader, refreshAccess, reportUnauthorized } from './auth'

/** A 4xx/5xx from the API: `{"error":{"code","message"}}` (api-contract.md). */
export class ApiError extends Error {
  status: number
  code?: string

  constructor(status: number, message: string, code?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

async function errorFrom(res: Response): Promise<ApiError> {
  try {
    const body = (await res.json()) as { error?: { code?: string; message?: string } }
    return new ApiError(res.status, body.error?.message ?? res.statusText, body.error?.code)
  } catch {
    return new ApiError(res.status, res.statusText)
  }
}

function request(path: string, init?: RequestInit): Promise<Response> {
  const headers = new Headers(init?.headers)
  const auth = getAuthHeader()
  if (auth) headers.set('Authorization', auth)
  return fetch(path, { ...init, headers })
}

/**
 * Calls an API route, adding the Authorization header when there is one.
 * On a 401 it refreshes the access token once and retries; if that also
 * fails (or there was nothing to refresh with) it reports the failure and
 * throws an `ApiError`.
 */
export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  let res = await request(path, init)
  if (res.status === 401 && (await refreshAccess())) {
    res = await request(path, init)
  }
  if (!res.ok) {
    if (res.status === 401) reportUnauthorized()
    throw await errorFrom(res)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export interface DashboardInfo {
  dashboard_id: number
  title: string
  owner: 'system' | 'user'
  group_id: number
  project_id?: number
  range?: string
  from?: string
  to?: string
  widgets: number
  archived_at?: string
}

/** One tab of a dashboard's group: enough to link to it and label it (tabs D18). */
export interface DashboardTab {
  dashboard_id: number
  title: string
}

export interface DashboardsResponse {
  timezone: string
  dev?: boolean
  errors?: { dir: string; message: string }[]
  dashboards: DashboardInfo[]
  /** Days until an archived dashboard is purged; absent when retention is off (D17a). */
  purge_after_days?: number
}

export interface Widget {
  widget_id: number
  dashboard_id: number
  name: string
  component: string | null
  title?: string
  width: number
  height: number
  props: Record<string, unknown>
  source: { type: 'sql' | 'md'; content: string }
  follows_project: boolean
  follows_range: boolean
  archived_at?: string
}

export interface DashboardDetail extends Omit<DashboardInfo, 'widgets'> {
  follows_project: boolean
  follows_range: boolean
  widgets: Widget[]
  /** The group's live members, in order, this dashboard included; always an array (tabs D18). */
  tabs: DashboardTab[]
}

export interface SqlData {
  columns: string[]
  rows: string[][]
  truncated: boolean
}

export interface MarkdownData {
  markdown: string
}

export interface WidgetData {
  widget_id: number
  source_type: 'sql' | 'md'
  project_id?: number
  from?: string
  to?: string
  cached_at?: string
  refresh_after?: string
  removed: boolean
  data: SqlData | MarkdownData | null
}

export interface WidgetDataQuery {
  project_id?: number
  from?: string
  to?: string
  fresh?: boolean
}

export interface SaveViewBody {
  project_id?: number
  range?: string
  from?: string
  to?: string
}

/**
 * The update_dashboard body a move sends: `after` names a live dashboard
 * (0 for first); `group_id` regroups, 0 taking the dashboard out as its
 * own group (tabs D6-D8; D15).
 */
export interface MoveBody {
  group_id?: number
  after?: number
}

export interface Project {
  project_id: number
  name: string
  archived?: boolean
}

export interface ProjectsResponse {
  projects: Project[]
}

export interface ComponentsResponse {
  source_types: ('md' | 'sql')[]
  components: unknown[]
}

function toQuery(q: object): string {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(q as Record<string, string | number | boolean | undefined>)) {
    if (v !== undefined) params.set(k, String(v))
  }
  const s = params.toString()
  return s ? `?${s}` : ''
}

export const endpoints = {
  dashboards: () => api<DashboardsResponse>('/api/dashboards'),
  dashboard: (id: number) => api<DashboardDetail>(`/api/dashboards/${id}`),
  widgetData: (id: number, q: WidgetDataQuery) => api<WidgetData>(`/api/widgets/${id}/data${toQuery(q)}`),
  components: () => api<ComponentsResponse>('/api/components'),
  saveView: (id: number, sel: SaveViewBody) =>
    api<{ status: string }>(`/api/dashboards/${id}/view`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(sel),
    }),
  /** Copies a dashboard, or its whole group (tabs D10-D11); never archives anything. */
  duplicate: (id: number, body: { whole_group?: boolean } = {}) =>
    api<DashboardDetail>(`/api/dashboards/${id}/duplicate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  /** Archives a dashboard, or its whole group (a system one needs `wholeGroup`) (D1). */
  archive: (id: number, wholeGroup = false) =>
    api<{ status: string }>(`/api/dashboards/${id}/archive`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(wholeGroup ? { whole_group: true } : {}),
    }),
  /** Restores a dashboard, or its whole group (D1). */
  restore: (id: number, wholeGroup = false) =>
    api<{ status: string }>(`/api/dashboards/${id}/restore`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(wholeGroup ? { whole_group: true } : {}),
    }),
  /** Moves a tab or a group by naming the dashboard it goes after (tabs D6-D7; D15). */
  move: (id: number, body: MoveBody) =>
    api<DashboardInfo>(`/api/dashboards/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  projects: () => api<ProjectsResponse>('/api/projects'),
  devVersion: () => api<{ version: string }>('/api/dev/version'),
}

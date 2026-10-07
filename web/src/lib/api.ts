import { getAuthHeader, refreshAccess, reportUnauthorized } from './auth'
import type { Filter } from './table-view'

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
 * Sends a request to an API route, adding the Authorization header when
 * there is one. On a 401 it refreshes the access token once and retries; if
 * that also fails (or there was nothing to refresh with) it reports the
 * failure. A non-2xx answer throws an `ApiError`.
 */
async function send(path: string, init?: RequestInit): Promise<Response> {
  let res = await request(path, init)
  if (res.status === 401 && (await refreshAccess())) {
    res = await request(path, init)
  }
  if (!res.ok) {
    if (res.status === 401) reportUnauthorized()
    throw await errorFrom(res)
  }
  return res
}

/** Calls an API route and reads its JSON answer (see `send` for auth and refusals). */
export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await send(path, init)
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

/** Calls an API route that answers a file, such as a PNG, and returns it as a Blob. */
async function apiBlob(path: string): Promise<Blob> {
  return (await send(path)).blob()
}

export interface DashboardInfo {
  dashboard_id: number
  title: string
  owner: 'system' | 'user'
  group_id: number
  /** The group's name, on every member of a named group; absent otherwise (group names D2). */
  group_title?: string
  project_id?: number
  range?: string
  from?: string
  to?: string
  widgets: number
  /** The dashboard's group is in the sidebar; the same on every member. */
  sidebar: boolean
  /** Every project created from now on gets this built-in as a tab; set by the release, always false on the user's own. */
  project_tab: boolean
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
  /** How often a dashboard with auto-refresh on reloads: max(REPORTING_CACHE_SECONDS, REPORTING_REFRESH_SECONDS); absent when both are 0. */
  auto_refresh_seconds?: number
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

/** One tab of a project's page after Setup. */
export interface ProjectTab {
  dashboard_id: number
  title: string
  owner: 'system' | 'user'
  group_id: number
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
  /** A remote table's place in the whole result: `matched` rows after the filters, `total` before them. */
  page?: PageInfo
}

export interface PageInfo {
  offset: number
  limit: number
  matched: number
  total: number
  sort?: string
  distinct?: string
  filters: Filter[]
}

export interface WidgetDataQuery {
  project_id?: number
  from?: string
  to?: string
  fresh?: boolean
  // A remote table's view (filters as JSON, sort as `column:asc|desc`); other widgets refuse them.
  filters?: string
  sort?: string
  distinct?: string
  offset?: number
  limit?: number
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
  allowed_origins: string[]
  attributes?: string[]
}

export interface IngestKey {
  project_id: number
  label: string
  key: string
  state: 'active' | 'disabled'
}

export type ArchiveAfter = '7d' | '30d' | '90d' | '365d' | 'project'
export type ShareState = 'live' | 'archived'

/** A public link to one widget's image over a range: its page, its two images, and when it archives itself. */
export interface WidgetShare {
  id: string
  url: string
  image_url: string
  image_2x_url: string
  /** Null once the widget is purged; the share keeps its title and range. */
  widget_id: number | null
  dashboard_id: number | null
  dashboard_title: string | null
  project_id: number
  project_name: string
  from: string
  to: string
  title: string
  /** Whether the share's page names the project, and the range: only what the widget followed. */
  caption_project: boolean
  caption_range: boolean
  created_at: string
  /** Null for a share that lives as long as its project. */
  archive_at: string | null
  archived_at: string | null
}

export type CapSetting = 'ATTRIBUTE_VALUES_TOP_N' | 'IDENTITIES_TOP_N'

/** A limit in force: a setting (with its environment variable and default) or one of the wire format's fixed limits. */
export interface Limit {
  group: 'retention' | 'caps' | 'ingest'
  name: string
  /** Absent for a fixed limit. */
  setting?: string
  /** In unit. */
  value: number
  /** Absent for a fixed limit. */
  default?: number
  /** Absent for a count or a plain number. */
  unit?: 'days' | 'bytes' | 'characters' | 'seconds'
  /** What 0 means when it is not the number: no cap, kept forever. */
  zero?: string
  description: string
}

export interface CapUsageRow {
  setting: CapSetting
  dimension: string
  cap: number
  max_values_per_day: number
  max_day: string
  days: number
  days_capped: number
  folded_share: number | null
}

export interface CapUsage {
  project_id: number
  from: string
  to: string
  dimensions: CapUsageRow[]
}

export interface UsageDay {
  day: string
  views: number
  events: number
  measures: number
  /** The project's size as the daily pass measured it that day; null on a day not measured. */
  total_bytes: number | null
  /** Declared attributes on a day the daily pass measured; null otherwise. */
  declared_attributes: number | null
  /** Distinct attribute keys and key/value pairs received, and values folded into (other): counted the night after, null until then. */
  attribute_keys: number | null
  attribute_values: number | null
  attribute_values_folded: number | null
}

export interface ProjectUsage {
  project_id: number
  series: UsageDay[]
  totals: { views: number; events: number; measures: number }
  last_received_at: string | null
  first_day: string | null
  raw_days: number
  rolled_up_days: number
  /** The latest the daily pass measured (it also runs at start), measured_at a UTC day; null until the first measurement. */
  size: { raw_bytes: number; aggregate_bytes: number; total_bytes: number; measured_at: string } | null
  /** Only computed for one project (asked with project_id); null in the all-projects answer. */
  unused_attributes: string[] | null
}

export interface UsageResponse {
  from: string
  to: string
  database_bytes: number
  /** The database file's size per day as the daily pass measured it; null on a day not measured. */
  database_series: { day: string; bytes: number | null }[]
  projects: ProjectUsage[]
}

export interface CreateProjectBody {
  name: string
  allowed_origins?: string[]
  attributes?: string[]
}

export interface CreatedProject {
  project_id: number
  key?: string
  snippet?: string
  note?: string
}

export interface IssuedKey {
  key: string
  snippet?: string
  status: string
  note?: string
}

export interface RangeQuery {
  from?: string
  to?: string
}

/** One attribute key a project received in a range (or declared without receiving). `max_values` is null before the nightly count. */
export interface ReceivedKey {
  key: string
  /** As the daily pass counted them: today's arrive the night after, so a key first received today has 0. */
  events: number
  max_values: number | null
  /** Whether any event in the range carried it; false for a declared key none carried. */
  received: boolean
  declared: boolean
}

/**
 * The keys a project received over a range, with the breakdown budget (`breakdowns_max` 0 is no limit).
 * `keys` holds the 500 busiest received keys and every declared one; `keys_total` counts every key received.
 */
export interface ReceivedAttributes {
  project_id?: number
  from: string
  to: string
  keys: ReceivedKey[]
  keys_total: number
  values_cap: number
  breakdowns_used: number
  breakdowns_max: number
}

export interface ProjectsResponse {
  projects: Project[]
}

/** A form as list_forms answers it (forms D8, D12): a draft until approved with the fields it keeps. */
export interface Form {
  name: string
  status: 'draft' | 'approved'
  purpose: string
  return_url: string
  /** Every field name submissions have sent, sorted. */
  fields: string[]
  /** What an approved form keeps, in column order; absent on a draft. */
  expected_fields?: string[]
  created_at: string
  /** A draft is archived at this time unless approved. */
  draft_until?: string
  approved_at?: string
  /** Submissions from this time on are refused. */
  closes_at?: string
  submissions: number
  last_submitted_at?: string
  archived: boolean
  archived_at?: string
}

/** list_forms' answer: the forms, and where a plain HTML form posts. */
export interface FormsResponse {
  forms: Form[]
  /** PUBLIC_URL + `/ingest/forms`, so a form's action is `<action_base>/<name>?key=…`; empty without PUBLIC_URL. */
  action_base: string
}

/** update_form's body: an omitted field is kept; `closes_at` null reopens. */
export interface FormUpdate {
  purpose?: string
  return_url?: string
  closes_at?: string | null
  expected_fields?: string[]
}

/** One page of a form's submissions table (D12a); `ids` holds each row's submission id, in row order. */
export interface SubmissionsPage {
  columns: string[]
  rows: string[][]
  ids: string[]
  matched: number
  total: number
  offset: number
  limit: number
}

/** list_submissions' arguments, a remote table's as widget_data takes them. */
export interface SubmissionsQuery {
  filters?: string
  sort?: string
  distinct?: string
  offset?: number
  limit?: number
}

/** The session a submission came in, snapshotted when it arrived (D9). */
export interface Visit {
  landing_path: string
  referrer: string
  utm_source: string
  utm_medium: string
  utm_campaign: string
  views: number
}

export interface Submission {
  id: string
  form: string
  received_at: string
  fields: Record<string, string>
  host: string
  path: string
  via: 'form' | 'json'
  visit?: Visit
}

/** A submission find_submissions found; the search reaches archived forms too. */
export interface FoundSubmission extends Submission {
  archived: boolean
}

/** delete_submissions takes exactly one selector: ids, a form's table filters, or a search. */
export type DeleteSubmissionsBody = { ids: string[] } | { form: string; filters: string } | { search: string }

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

function json(method: string, body: unknown): RequestInit {
  return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
}

function formPath(projectId: number, name: string): string {
  return `/api/projects/${projectId}/forms/${encodeURIComponent(name)}`
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
  /** Copies a dashboard as one of its own, into group `group_id` as a tab, or its whole group (tabs D10-D11); never archives anything. */
  duplicate: (id: number, body: { whole_group?: boolean; group_id?: number } = {}) =>
    api<DashboardDetail>(`/api/dashboards/${id}/duplicate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  /** Archives a dashboard of the user's, or its whole group (D1); a built-in one is refused (see `setSidebar`). */
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
  /** Names the dashboard's group (group names D5); a name is replaced, never cleared, and no dashboard title changes. */
  renameGroup: (id: number, title: string) =>
    api<DashboardInfo>(`/api/dashboards/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ whole_group: true, title }),
    }),
  /** Puts a built-in dashboard's whole group in or out of the sidebar; a built-in is never archived, and your own are always in it (project tabs D5). */
  setSidebar: (id: number, sidebar: boolean) => api<DashboardInfo>(`/api/dashboards/${id}`, json('PATCH', { sidebar })),
  /** A project page's tabs after Setup, in order. */
  projectTabs: (projectId: number) => api<{ tabs: ProjectTab[] }>(`/api/projects/${projectId}/tabs`),
  /** Shows a dashboard as a tab of the project; `after` places one of the user's own, omitted puts it last. */
  addProjectTab: (projectId: number, body: { dashboard_id: number; after?: number }) =>
    api<{ tabs: ProjectTab[] }>(`/api/projects/${projectId}/tabs`, json('POST', body)),
  removeProjectTab: (projectId: number, dashboardId: number) =>
    api<{ tabs: ProjectTab[] }>(`/api/projects/${projectId}/tabs/${dashboardId}/remove`, json('POST', {})),
  /** Reorders the user's own tabs: after `after` (one of the project's own tabs), 0 first. */
  moveProjectTab: (projectId: number, dashboardId: number, after: number) =>
    api<{ tabs: ProjectTab[] }>(`/api/projects/${projectId}/tabs/${dashboardId}/move`, json('POST', { after })),
  projects: () => api<ProjectsResponse>('/api/projects'),
  keys: (projectId?: number) => api<{ keys: IngestKey[] }>(`/api/keys${toQuery({ project_id: projectId })}`),
  createProject: (body: CreateProjectBody) => api<CreatedProject>('/api/projects', json('POST', body)),
  updateProject: (id: number, body: Partial<CreateProjectBody>) => api<{ project_id: number }>(`/api/projects/${id}`, json('PATCH', body)),
  moveProject: (id: number, after: number) => api<{ status: string }>(`/api/projects/${id}/move`, json('POST', { after })),
  archiveProject: (id: number) => api<{ status: string }>(`/api/projects/${id}/archive`, json('POST', {})),
  restoreProject: (id: number) => api<{ status: string }>(`/api/projects/${id}/restore`, json('POST', {})),
  issueKey: (id: number, label: string) => api<IssuedKey>(`/api/projects/${id}/keys`, json('POST', { label })),
  disableKey: (id: number, label: string) =>
    api<{ status: string }>(`/api/projects/${id}/keys/${encodeURIComponent(label)}/disable`, json('POST', {})),
  enableKey: (id: number, label: string) =>
    api<{ status: string }>(`/api/projects/${id}/keys/${encodeURIComponent(label)}/enable`, json('POST', {})),
  limits: () => api<{ limits: Limit[] }>('/api/limits'),
  usage: (q: RangeQuery & { project_id?: number }) => api<UsageResponse>(`/api/usage${toQuery(q)}`),
  receivedAttributes: (q: RangeQuery & { project_id?: number }) =>
    api<ReceivedAttributes>(`/api/received-attributes${toQuery(q)}`),
  capUsage: (id: number, q: RangeQuery) => api<CapUsage>(`/api/projects/${id}/cap-usage${toQuery(q)}`),
  widgetShares: (q: { widget_id?: number; state?: ShareState }) => api<{ shares: WidgetShare[] }>(`/api/widget-shares${toQuery(q)}`),
  // No Content-Type here: the browser sets multipart/form-data with its boundary for a FormData body.
  createWidgetShare: (form: FormData) => api<WidgetShare>('/api/widget-shares', { method: 'POST', body: form }),
  updateWidgetShare: (id: string, archive_after: ArchiveAfter) =>
    api<WidgetShare>(`/api/widget-shares/${id}`, json('PATCH', { archive_after })),
  archiveWidgetShare: (id: string) => api<WidgetShare>(`/api/widget-shares/${id}/archive`, json('POST', {})),
  restoreWidgetShare: (id: string, archive_after: ArchiveAfter) =>
    api<WidgetShare>(`/api/widget-shares/${id}/restore`, json('POST', { archive_after })),
  /** A share's 1x PNG in any state; the public image URL answers 404 once the share is archived. */
  widgetShareImage: (id: string) => apiBlob(`/api/widget-shares/${id}/image`),
  devVersion: () => api<{ version: string }>('/api/dev/version'),
  /** A project's forms, drafts first; `archived` lists the archived ones instead. */
  forms: (projectId: number, archived = false) =>
    api<FormsResponse>(`/api/projects/${projectId}/forms${toQuery({ archived: archived || undefined })}`),
  approveForm: (projectId: number, name: string, expected_fields: string[]) =>
    api<{ status: string }>(`${formPath(projectId, name)}/approve`, json('POST', { expected_fields })),
  updateForm: (projectId: number, name: string, body: FormUpdate) =>
    api<{ status: string }>(formPath(projectId, name), json('PATCH', body)),
  archiveForm: (projectId: number, name: string) => api<{ status: string }>(`${formPath(projectId, name)}/archive`, json('POST', {})),
  restoreForm: (projectId: number, name: string) => api<{ status: string }>(`${formPath(projectId, name)}/restore`, json('POST', {})),
  submissions: (projectId: number, name: string, q: SubmissionsQuery) =>
    api<SubmissionsPage>(`${formPath(projectId, name)}/submissions${toQuery(q)}`),
  submission: (projectId: number, name: string, id: string) =>
    api<Submission>(`${formPath(projectId, name)}/submissions/${encodeURIComponent(id)}`),
  /** Every active form's submissions with a field value containing `search`, for an erasure request. */
  findSubmissions: (projectId: number, q: { search: string; limit?: number; cursor?: string }) =>
    api<{ submissions: FoundSubmission[]; next_cursor?: string }>(`/api/projects/${projectId}/submissions${toQuery(q)}`),
  deleteSubmissions: (projectId: number, body: DeleteSubmissionsBody) =>
    api<{ deleted: number }>(`/api/projects/${projectId}/submissions/delete`, json('POST', body)),
  /** The submissions table as CSV, every row the filters match, read with the console's auth. */
  exportSubmissions: (projectId: number, name: string, q: { filters?: string; sort?: string }) =>
    apiBlob(`${formPath(projectId, name)}/submissions.csv${toQuery(q)}`),
}

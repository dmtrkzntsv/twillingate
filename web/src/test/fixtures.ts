import type { DashboardDetail, DashboardInfo, DashboardsResponse, DashboardTab, Project, Widget, WidgetData } from '@/lib/api'

// A realistic instance for page tests: the five system reports as one
// group, a lone user dashboard, an archived one, a two-tab user group,
// three projects (one archived), and a Views report that uses every
// widget kind at its usual size.

const DAYS = ['2026-09-20', '2026-09-21', '2026-09-22', '2026-09-23', '2026-09-24', '2026-09-25', '2026-09-26']

function info(dashboard_id: number, title: string, owner: 'system' | 'user', widgets: number, extra: Partial<DashboardInfo> = {}): DashboardInfo {
  return { dashboard_id, title, owner, group_id: dashboard_id, widgets, ...extra }
}

export function dashboardsList(over: Partial<DashboardsResponse> = {}): DashboardsResponse {
  return {
    timezone: 'UTC',
    dashboards: [
      info(1, 'Views', 'system', 17, { group_id: 1, project_id: 7, range: '7d' }),
      info(2, 'Product', 'system', 2, { group_id: 1 }),
      info(3, 'Users', 'system', 1, { group_id: 1 }),
      info(4, 'Groups', 'system', 1, { group_id: 1 }),
      info(5, 'Retention', 'system', 1, { group_id: 1 }),
      info(10, 'Launch week', 'user', 2),
      info(11, 'Old experiment', 'user', 1, { archived_at: '2026-09-01T00:00:00Z' }),
      info(13, 'Marketing', 'user', 1, { group_id: 13 }),
      info(14, 'Funnel', 'user', 1, { group_id: 13 }),
    ],
    ...over,
  }
}

export const projects: Project[] = [
  { project_id: 7, name: 'shop', allowed_origins: ['https://shop.example'] },
  { project_id: 1, name: 'blog', allowed_origins: ['https://blog.example'] },
  { project_id: 3, name: 'legacy', archived: true, allowed_origins: [] },
]

let nextId = 100

function widget(dashboard_id: number, component: string, title: string, width: number, height: number, extra: Partial<Widget> = {}): Widget {
  return {
    widget_id: nextId++,
    dashboard_id,
    name: title.toLowerCase().replace(/\W+/g, '_'),
    component,
    title,
    width,
    height,
    props: {},
    source: { type: component === 'markdown' ? 'md' : 'sql', content: '…' },
    follows_project: component !== 'markdown',
    follows_range: component !== 'markdown',
    ...extra,
  }
}

function detail(info: DashboardInfo, widgets: Widget[], opts: { tabs?: DashboardTab[]; follows?: boolean } = {}): DashboardDetail {
  const tabs = opts.tabs ?? [{ dashboard_id: info.dashboard_id, title: info.title }]
  const follows = opts.follows ?? true
  return { ...info, follows_project: follows, follows_range: follows, widgets, tabs }
}

const list = dashboardsList().dashboards

/** The system group's tabs (ids 1-5), the same list `get_dashboard` returns from every member. */
const systemTabs: DashboardTab[] = list.slice(0, 5).map((d) => ({ dashboard_id: d.dashboard_id, title: d.title }))

/** The Marketing/Funnel user group's tabs (ids 13-14). */
const marketingTabs: DashboardTab[] = [
  { dashboard_id: 13, title: 'Marketing' },
  { dashboard_id: 14, title: 'Funnel' },
]

export const views = detail(list[0], [
  widget(1, 'stat', 'Visitors', 3, 3),
  widget(1, 'stat', 'Views', 3, 3),
  widget(1, 'stat', 'Bounce rate', 3, 3, { props: { format: 'percent' } }),
  widget(1, 'stat', 'Avg. session', 3, 3, { props: { format: 'duration' } }),
  widget(1, 'line', 'Visitors per day', 6, 8),
  widget(1, 'area', 'Views by kind', 6, 8),
  widget(1, 'bar', 'Views per day', 6, 8),
  widget(1, 'combo', 'Sessions and bounces', 6, 8),
  widget(1, 'bar_list', 'Top pages', 6, 8),
  widget(1, 'table', 'Referrers', 6, 10),
  widget(1, 'pie', 'Devices', 4, 8),
  widget(1, 'radial', 'Goal', 4, 8),
  widget(1, 'radar', 'Browsers', 4, 8),
  widget(1, 'map', 'Countries', 6, 8),
  widget(1, 'treemap', 'Sections', 6, 8),
  widget(1, 'funnel', 'Signup funnel', 6, 8),
  widget(1, 'scatter', 'Pages vs time', 6, 8),
  widget(1, 'heatmap', 'Hour by weekday', 6, 10),
  widget(1, 'calendar', 'Daily visitors', 12, 4),
  widget(1, 'markdown', 'About this report', 12, 2),
], { tabs: systemTabs })

export const product = detail(list[1], [widget(2, 'stat', 'Events', 3, 3), widget(2, 'line', 'Events per day', 9, 8)], {
  tabs: systemTabs,
})

export const launchWeek = detail(
  list[5],
  [
    widget(10, 'stat', 'Signups during launch', 4, 3, { follows_project: false, follows_range: false }),
    widget(10, 'markdown', 'Notes', 8, 3),
  ],
  { follows: false }
)

export const empty = detail(info(12, 'Scratch', 'user', 0), [])

// Archived, lone user dashboard (tabs D17, D18): still fetchable by id,
// just gone from the sidebar.
export const oldExperiment = detail(list[6], [], { tabs: [{ dashboard_id: 11, title: 'Old experiment' }] })

export const marketing = detail(list[7], [widget(13, 'stat', 'Leads', 3, 3)], { tabs: marketingTabs })
export const funnel = detail(list[8], [widget(14, 'stat', 'Conversions', 3, 3)], { tabs: marketingTabs })

export const details: Record<number, DashboardDetail> = {
  1: views,
  2: product,
  3: detail(list[2], [widget(3, 'stat', 'Users', 3, 3)], { tabs: systemTabs }),
  4: detail(list[3], [widget(4, 'stat', 'Groups', 3, 3)], { tabs: systemTabs }),
  5: detail(list[4], [widget(5, 'heatmap', 'Cohorts', 12, 10)], { tabs: systemTabs }),
  10: launchWeek,
  11: oldExperiment,
  12: empty,
  13: marketing,
  14: funnel,
}

function sql(columns: string[], rows: (string | number)[][]) {
  return { columns, rows: rows.map((r) => r.map(String)), truncated: false }
}

const perDay = (f: (i: number) => number) => DAYS.map((d, i) => [d, f(i)])

const DATA: Record<string, WidgetData['data']> = {
  stat: sql(['value', 'previous'], [[1234, 1100]]),
  line: sql(['x', 'y'], perDay((i) => 100 + i * 10)),
  area: sql(['x', 'series', 'y'], DAYS.flatMap((d, i) => [[d, 'web', 80 + i], [d, 'app', 20 + i]])),
  bar: sql(['x', 'y'], perDay((i) => 300 + i)),
  combo: sql(['x', 'bar', 'line'], DAYS.map((d, i) => [d, 50 + i, 20 - i])),
  bar_list: sql(['label', 'value'], [['/', 900], ['/pricing', 400], ['/docs', 250]]),
  table: sql(['referrer', 'visitors'], [['google.com', 500], ['news.ycombinator.com', 120]]),
  pie: sql(['label', 'value'], [['desktop', 60], ['mobile', 35], ['tablet', 5]]),
  radial: sql(['label', 'value', 'max'], [['signups', 70, 100]]),
  radar: sql(['axis', 'value'], [['Chrome', 60], ['Safari', 25], ['Firefox', 10], ['Edge', 5]]),
  map: sql(['country', 'value'], [['US', 500], ['DE', 200], ['FR', 90]]),
  treemap: sql(['label', 'value'], [['blog', 300], ['docs', 200], ['app', 100]]),
  funnel: sql(['step', 'value'], [['visit', 1000], ['signup', 200], ['paid', 30]]),
  scatter: sql(['x', 'y'], [[1, 30], [2, 45], [3, 80]]),
  heatmap: sql(['x', 'y', 'value'], [['Mon', '09', 5], ['Tue', '10', 8], ['Wed', '11', 3]]),
  calendar: sql(['day', 'value'], perDay((i) => i * 3)),
}

/** The API's answer for a widget: realistic rows for its kind, fresh enough to refresh. */
export function answerFor(w: Widget): WidgetData {
  const now = Date.now()
  return {
    widget_id: w.widget_id,
    source_type: w.source.type,
    removed: false,
    ...(w.source.type === 'sql'
      ? {
          cached_at: new Date(now - 20 * 60_000).toISOString(),
          refresh_after: new Date(now - 5 * 60_000).toISOString(),
        }
      : {}),
    data: w.component === 'markdown' ? { markdown: 'Counts **unique visitors** per day.' } : DATA[w.component!],
  }
}

/** Every widget in the fixtures, by id. */
export const widgetsById = new Map(Object.values(details).flatMap((d) => d.widgets.map((w) => [w.widget_id, w] as const)))

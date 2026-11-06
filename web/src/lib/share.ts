import type { ArchiveAfter, Widget, WidgetShare } from './api'

/** What a widget needs to be shared or downloaded: the page's project and range, and whether it may write. */
export interface ShareContext {
  /**
   * The project the page shows; on a dashboard with no project switcher,
   * the one project its query widgets are pinned to (pinnedProject), else
   * absent: a share then has no project, so it never names one and is
   * never deleted with one.
   */
  project?: { id: number; name: string }
  /** The range's first and last day, YYYY-MM-DD: the page's, or the default 7 days with no range switcher. */
  from: string
  to: string
  /** Whether the page hands its range to its widgets (it has a range switcher). */
  rangeShown: boolean
}

/**
 * What a widget's card and share page name: the project and the range only
 * when the widget follows them, so a widget pinned to its own project or
 * range is never captioned with the page's. Spread into a ShareCard.
 */
export function shareCaption(
  widget: Pick<Widget, 'follows_project' | 'follows_range'>,
  ctx: ShareContext
): { projectName?: string; from?: string; to?: string } {
  return {
    ...(ctx.project && widget.follows_project ? { projectName: ctx.project.name } : {}),
    ...(ctx.rangeShown && widget.follows_range ? { from: ctx.from, to: ctx.to } : {}),
  }
}

/**
 * The one project a dashboard's structured queries are pinned to: each
 * query widget names it as its `project_id`, and no query names another
 * or none. SQL and markdown widgets name no project and are passed over;
 * undefined when there is no query widget, or no single project.
 */
export function pinnedProject(widgets: readonly Pick<Widget, 'source'>[]): number | undefined {
  const ids = new Set<unknown>()
  for (const { source } of widgets) {
    if (source.type !== 'query') continue
    try {
      ids.add((JSON.parse(source.content) as { project_id?: unknown } | null)?.project_id)
    } catch {
      return undefined
    }
  }
  const [id] = ids
  return ids.size === 1 && typeof id === 'number' ? id : undefined
}

/**
 * The archive-after choices, in the order the pickers list them: the
 * periods, then no date, which is the project's lifetime for a share with
 * a project and "Never" for one without.
 */
export function archiveAfterChoices(hasProject: boolean): { value: ArchiveAfter; label: string }[] {
  return [
    { value: '7d', label: '1 week' },
    { value: '30d', label: '1 month' },
    { value: '90d', label: '3 months' },
    { value: '365d', label: '1 year' },
    hasProject ? { value: 'project', label: 'Project lifetime' } : { value: 'never', label: 'Never' },
  ]
}

export const DEFAULT_ARCHIVE_AFTER: ArchiveAfter = '30d'

/** A YYYY-MM-DD day as a UTC date, or undefined when it is not a real day. */
function parseDay(day: string): Date | undefined {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) return undefined
  const d = new Date(`${day}T00:00:00Z`)
  return Number.isNaN(d.getTime()) || d.toISOString().slice(0, 10) !== day ? undefined : d
}

function format(d: Date, year: boolean): string {
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', ...(year ? { year: 'numeric' } : {}), timeZone: 'UTC' })
}

/**
 * Two YYYY-MM-DD days as "Sep 5 – Oct 4, 2026", the way the share page words
 * them (the Go `rangeInWords`): the year on both ends only when they differ,
 * one day alone when they are the same, anything that is not a date as it is.
 */
export function rangeInWords(from: string, to: string): string {
  const f = parseDay(from)
  const t = parseDay(to)
  if (!f || !t) return `${from} – ${to}`
  if (from === to) return format(t, true)
  if (f.getUTCFullYear() === t.getUTCFullYear()) return `${format(f, false)} – ${format(t, true)}`
  return `${format(f, true)} – ${format(t, true)}`
}

/**
 * When a share archives itself: "Nov 4" (UTC), with the year when it is not
 * this one ("Jan 4, 2027"), or with no date "Project lifetime", or "Never"
 * for a share with no project.
 */
export function archiveLabel(share: WidgetShare, now: Date = new Date()): string {
  if (!share.archive_at) return share.project_id === null ? 'Never' : 'Project lifetime'
  const d = new Date(share.archive_at)
  return format(d, d.getUTCFullYear() !== now.getUTCFullYear())
}

function escapeAttr(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/** The HTML to paste into a page: the image, linked to the share page, with the 2x image for dense screens. */
export function embedCode(share: WidgetShare): string {
  return `<a href="${share.url}"><img src="${share.image_url}" srcset="${share.image_2x_url} 2x" alt="${escapeAttr(share.title)}" width="600" height="315"></a>`
}

import { useState, type ReactNode } from 'react'
import { useQueries, useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import { DashboardGroup, LoneDashboard, type GroupRow } from '@/components/DashboardGroup'
import { RestoreShareDialog } from '@/components/share/RestoreShareDialog'
import { Button } from '@/components/ui/button'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import { useFormActions } from '@/hooks/use-form-actions'
import { useProjectActions } from '@/hooks/use-project-actions'
import { useShareImage } from '@/hooks/use-share-image'
import { useWidgetEdits } from '@/hooks/use-widget-edits'
import type { DashboardInfo, Form, ListedWidget, Project, WidgetShare } from '@/lib/api'
import { groupName, purgeDate } from '@/lib/arrange'
import { plural } from '@/lib/format'
import { archivedWidgetsQuery, dashboardsQuery, formsQuery, projectsQuery, widgetSharesQuery } from '@/lib/queries'
import { formatPurgeDate } from '@/lib/time'

interface Group {
  groupId: number
  /** Every member, live and archived, in list order. */
  members: DashboardInfo[]
}

/**
 * The user's dashboards grouped by `group_id`, in list order, keeping only
 * the groups with an archived member (D17a). A system group is never
 * archived, only hidden from the sidebar, and comes back from the
 * Dashboards gallery, so it is never here.
 */
function archivedGroups(dashboards: DashboardInfo[]): Group[] {
  const groups = new Map<number, Group>()
  for (const d of dashboards) {
    if (d.owner !== 'user') continue
    const g = groups.get(d.group_id)
    if (g) g.members.push(d)
    else groups.set(d.group_id, { groupId: d.group_id, members: [d] })
  }
  return [...groups.values()].filter((g) => g.members.some((m) => m.archived_at))
}

/** An archived widget's dashboard and the widgets archived out of it. */
interface WidgetGroup {
  dashboard: DashboardInfo
  widgets: ListedWidget[]
}

/**
 * The archived widgets of the user's live dashboards, grouped by dashboard in
 * first-seen order, each group's widgets in their page order. A widget of an
 * archived dashboard comes back with the dashboard, and a system dashboard's
 * are never archived, so neither is listed.
 */
function archivedWidgetGroups(widgets: ListedWidget[]): WidgetGroup[] {
  const groups = new Map<number, WidgetGroup>()
  for (const w of widgets) {
    if (!w.archived_at || w.dashboard.archived_at || w.dashboard.owner !== 'user') continue
    const g = groups.get(w.dashboard.dashboard_id)
    if (g) g.widgets.push(w)
    else groups.set(w.dashboard.dashboard_id, { dashboard: w.dashboard, widgets: [w] })
  }
  for (const g of groups.values()) g.widgets.sort((a, b) => a.position - b.position)
  return [...groups.values()]
}

/**
 * An archived share's image, 1x. Its public URL answers 404 while it is
 * archived, so it is read through the console's authenticated image route;
 * until that arrives, or if it fails, a blank tile of the same size holds
 * the row's layout.
 */
function ShareThumb({ share }: { share: WidgetShare }) {
  const { url } = useShareImage(share.id)
  const size = 'aspect-[1200/630] w-20 rounded border sm:w-24'
  if (!url) return <div aria-hidden className={`${size} bg-muted`} />
  return <img src={url} alt={`Shared image of ${share.title}`} className={`${size} object-cover`} />
}

/**
 * `/archive`: every group of the user's with a dashboard out of the
 * sidebar, the only page that reaches one once it is archived (D17a). A
 * group of one is a single row; a larger group is a card listing its
 * tabs, the live ones too, so it reads as the dashboard it is in the
 * sidebar. Each archived tab restores on its own, and "Restore all"
 * brings back every archived tab at once. Its buttons wait while one
 * action runs; reporting dev, which takes no writes, shows none.
 *
 * Below the dashboards, an Archived widgets section lists the widgets
 * archived out of the user's live dashboards, grouped under each one with
 * Restore. It is left out when there are none, and in reporting dev, which
 * takes no writes.
 *
 * Then a Projects section lists the archived projects, in list order, each
 * linking to its page with its purge date and Restore. It is left out when there are none, and
 * in reporting dev, which takes no writes.
 *
 * Then a Shares section lists the archived widget shares,
 * most recently archived first, each restorable with a new archive date
 * (D9). It is left out when there are none, and in reporting dev, which has
 * no shares.
 *
 * Last, a Forms section lists each live project's archived forms with
 * Restore (forms D11, D12); an archived project hides its forms, so its
 * own are not asked for. Reporting dev serves no forms.
 */
/** One kind of archived thing: a heading over its rows. */
function ArchiveSection({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section aria-labelledby={id} className="flex flex-col gap-3">
      <h2 id={id} className="text-base font-semibold">
        {title}
      </h2>
      <ul className="flex flex-col gap-2">{children}</ul>
    </section>
  )
}

interface RowProps {
  /** Drawn before the text, in a column of its own. */
  thumb?: ReactNode
  name: ReactNode
  /** A line under the name: what it belongs to. */
  under?: string
  status: ReactNode
  /** Undefined offers no Restore. */
  onRestore?: () => void
  disabled?: boolean
}

/** An archived project, share or form: its name, its status line, and Restore. */
function ArchivedRow({ thumb, name, under, status, onRestore, disabled }: RowProps) {
  return (
    <li className={`grid ${thumb ? 'grid-cols-[auto_minmax(0,1fr)_auto]' : 'grid-cols-[minmax(0,1fr)_auto]'} items-center gap-3 rounded-lg border p-3`}>
      {thumb}
      <div className="min-w-0">
        {name}
        {under !== undefined && (
          <div className="truncate text-xs text-muted-foreground" title={under}>
            {under}
          </div>
        )}
        <div className="text-xs break-words text-muted-foreground">{status}</div>
      </div>
      {onRestore && (
        <Button variant="outline" size="sm" disabled={disabled} onClick={onRestore}>
          Restore
        </Button>
      )}
    </li>
  )
}

export default function Archive() {
  const { data } = useQuery(dashboardsQuery)
  const dashboards = data?.dashboards ?? []
  const writable = data?.dev !== true
  const purgeDays = data?.purge_after_days
  const { restore, pending } = useDashboardActions()
  const groups = archivedGroups(dashboards)
  const sharesQ = useQuery({ ...widgetSharesQuery({ state: 'archived' }), enabled: data !== undefined && writable })
  const shares = sharesQ.data?.shares ?? []
  const widgetsQ = useQuery({ ...archivedWidgetsQuery, enabled: data !== undefined && writable })
  const widgetGroups = archivedWidgetGroups(widgetsQ.data?.widgets ?? [])
  const widgetEdits = useWidgetEdits()
  // The widgets whose restore is out: their Restore waits, so a double click restores once.
  const [restoringWidgets, setRestoringWidgets] = useState<ReadonlySet<number>>(new Set())
  const restoreWidget = async (w: ListedWidget) => {
    setRestoringWidgets((s) => new Set(s).add(w.widget_id))
    try {
      await widgetEdits.restore({ widget_id: w.widget_id, dashboard_id: w.dashboard_id })
    } finally {
      setRestoringWidgets((s) => {
        const next = new Set(s)
        next.delete(w.widget_id)
        return next
      })
    }
  }
  const [restoring, setRestoring] = useState<WidgetShare>()
  const projectsQ = useQuery({ ...projectsQuery, enabled: data !== undefined && writable })
  const live: Project[] = (projectsQ.data?.projects ?? []).filter((p) => !p.archived)
  // The sidebar reads the projects in reporting dev too, so the cache can hold them there.
  const archivedProjects: Project[] = writable ? (projectsQ.data?.projects ?? []).filter((p) => p.archived) : []
  const projectActions = useProjectActions()
  const formsQs = useQueries({ queries: live.map((p) => ({ ...formsQuery(p.project_id, true), enabled: writable })) })
  const archivedForms: { project: Project; form: Form }[] = live.flatMap((p, i) =>
    (formsQs[i]?.data?.forms ?? []).map((form) => ({ project: p, form }))
  )
  const formsLoading = projectsQ.isLoading || formsQs.some((q) => q.isLoading)
  const formActions = useFormActions()

  /** A tab's status line: archived (and when it goes) or still in the sidebar. */
  const status = (d: DashboardInfo) => {
    if (!d.archived_at) return 'in the sidebar'
    const purged = purgeDate(d.archived_at, purgeDays)
    return purged ? `archived · deleted on ${formatPurgeDate(purged)}` : 'archived'
  }

  const restoreButton = (label: string, onClick: () => void) =>
    writable && (
      <Button variant="outline" size="sm" disabled={pending} onClick={onClick}>
        {label}
      </Button>
    )

  const renderGroup = (g: Group) => {
    const archived = g.members.filter((m) => m.archived_at)
    const rows: GroupRow[] = g.members.map((d) => ({
      ...d,
      detail: status(d),
      muted: !d.archived_at,
      action: d.archived_at && restoreButton('Restore', () => void restore(d.dashboard_id)),
    }))
    if (rows.length === 1) return <LoneDashboard key={g.groupId} row={{ ...rows[0], title: groupName(g.members) }} />
    const whole = archived.length === g.members.length
    return (
      <DashboardGroup
        key={g.groupId}
        title={groupName(g.members)}
        meta={`${g.members.length} tabs · ${whole ? 'all archived' : `${archived.length} archived`}`}
        action={archived.length > 1 && restoreButton('Restore all', () => void restore(archived[0].dashboard_id, true))}
        rows={rows}
      />
    )
  }

  /** An archived form's lead-in to its status line: how many submissions go with it. */
  const submissionCount = (f: Form) => `${plural(f.submissions, 'submission')} · `

  /** An archived row's status line: archived, and when it goes, the date kept on one line. */
  const archivedStatus = (archived_at: string | null | undefined, prefix = '') => {
    const purged = archived_at ? purgeDate(archived_at, purgeDays) : undefined
    if (!purged) return `${prefix}archived`
    return (
      <>
        {prefix}archived · deleted on <span className="whitespace-nowrap">{formatPurgeDate(purged)}</span>
      </>
    )
  }

  return (
    <AppShell dashboards={dashboards} currentId={0} readOnly={data?.dev === true}>
      <TopBar>
        <Crumbs items={[{ label: 'Archive' }]} />
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        <header className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold tracking-tight">Archive</h1>
          <p className="max-w-prose text-sm text-muted-foreground">
            Archived dashboards are out of the sidebar. Restore one to put it back.
            {purgeDays ? ` They are deleted after ${purgeDays} days.` : ''} A hidden built-in dashboard comes back from
            Gallery › Dashboards.
            Archived projects receive nothing until restored. Archived shares answer 404 until restored. Archived forms refuse submissions.
          </p>
        </header>
        {data !== undefined && groups.length === 0 && archivedProjects.length === 0 && widgetGroups.length === 0 && shares.length === 0 && archivedForms.length === 0 && !widgetsQ.isLoading && !sharesQ.isLoading && !formsLoading && (
          <p className="text-sm text-muted-foreground">Nothing archived.</p>
        )}
        {groups.length > 0 && <ul className="flex flex-col gap-2">{groups.map(renderGroup)}</ul>}
        {archivedProjects.length > 0 && (
          <ArchiveSection id="archived-projects" title="Projects">
            {archivedProjects.map((p) => (
              <ArchivedRow
                key={p.project_id}
                name={
                  <div className="truncate font-medium" title={p.name}>
                    <Link to={`/projects/${p.project_id}`} className="underline-offset-2 hover:underline">
                      {p.name}
                    </Link>
                  </div>
                }
                status={archivedStatus(p.archived_at)}
                disabled={projectActions.pending}
                onRestore={() => void projectActions.restore(p.project_id)}
              />
            ))}
          </ArchiveSection>
        )}
        {widgetGroups.length > 0 && (
          <ArchiveSection id="archived-widgets" title="Archived widgets">
            {widgetGroups.map(({ dashboard, widgets }) => (
              <li key={dashboard.dashboard_id} aria-label={dashboard.title} className="flex flex-col rounded-lg border">
                <div className="border-b bg-muted/40 p-3">
                  <h3 className="truncate font-medium">
                    <Link to={`/dashboards/${dashboard.dashboard_id}`} className="underline-offset-2 hover:underline">
                      {dashboard.title}
                    </Link>
                  </h3>
                </div>
                <ul className="flex flex-col divide-y">
                  {widgets.map((w) => (
                    <li key={w.widget_id} className="flex items-center justify-between gap-3 px-3 py-2">
                      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                        <span className="truncate text-sm font-medium" title={w.title ?? w.name}>
                          {w.title ?? w.name}
                        </span>
                        <span className="text-xs text-muted-foreground">{archivedStatus(w.archived_at)}</span>
                      </div>
                      <Button variant="outline" size="sm" disabled={restoringWidgets.has(w.widget_id)} onClick={() => void restoreWidget(w)}>
                        Restore
                      </Button>
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ArchiveSection>
        )}
        {shares.length > 0 && (
          <ArchiveSection id="archived-shares" title="Shares">
            {shares.map((s) => (
              <ArchivedRow
                key={s.id}
                thumb={<ShareThumb share={s} />}
                name={
                  <div className="truncate font-medium" title={s.title}>
                    {s.title}
                  </div>
                }
                under={s.project_name}
                status={archivedStatus(s.archived_at)}
                onRestore={writable ? () => setRestoring(s) : undefined}
              />
            ))}
          </ArchiveSection>
        )}
        {archivedForms.length > 0 && (
          <ArchiveSection id="archived-forms" title="Forms">
            {archivedForms.map(({ project, form }) => (
              <ArchivedRow
                key={`${project.project_id}/${form.name}`}
                name={<div className="font-mono text-sm font-medium break-all">{form.name}</div>}
                under={project.name}
                status={archivedStatus(form.archived_at, submissionCount(form))}
                disabled={formActions.pending}
                onRestore={() => void formActions.restore(project.project_id, form.name)}
              />
            ))}
          </ArchiveSection>
        )}
        <RestoreShareDialog share={restoring} onClose={() => setRestoring(undefined)} />
      </div>
    </AppShell>
  )
}

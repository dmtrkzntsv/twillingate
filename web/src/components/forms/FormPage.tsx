import { useEffect, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { ArchiveIcon, ArchiveRestoreIcon, ArrowLeftIcon, DownloadIcon, InboxIcon, XIcon } from 'lucide-react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import WidgetFrame from '@/components/WidgetFrame'
import LoadError from '@/components/projects/LoadError'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import Table from '@/components/widgets/table'
import { useFormActions } from '@/hooks/use-form-actions'
import { useMarkFormSeen } from '@/hooks/use-mark-seen'
import { useStoredState } from '@/hooks/use-stored-state'
import { ApiError, endpoints, type Form, type Project, type SubmissionsPage, type SubmissionsQuery } from '@/lib/api'
import { downloadBlob } from '@/lib/capture'
import { closingDone, formatDay, formStatus, purgeDays as purgeDaysOf, purgeNote as purgeNoteFor, toCSV, type PurgeDays } from '@/lib/forms'
import { FORMS_ID, rangeParams, tabPath } from '@/lib/project-tabs'
import { dashboardsQuery, formsQuery, submissionsQuery } from '@/lib/queries'
import { liveFilters, parseView, type Filter, type TableView } from '@/lib/table-view'
import ApproveDialog from './ApproveDialog'
import FormMenu from './FormMenu'
import SettingsDialog from './SettingsDialog'
import SubmissionDrawer from './SubmissionDrawer'

/**
 * The table's arguments for a view: once an answer has named the columns,
 * only the filters and sort on columns it has (the server refuses the
 * rest); before one has, the view whole. Empty parts are left out.
 */
function viewArgs(view: TableView, columns: string[] | undefined, archived: boolean): SubmissionsQuery {
  const filters = columns ? liveFilters(view, columns) : view.filters
  const sort = view.sort && (!columns || columns.includes(view.sort.column)) ? view.sort : null
  const q: SubmissionsQuery = archived ? { archived: true } : {}
  if (filters.length > 0) q.filters = JSON.stringify(filters)
  if (sort) q.sort = `${sort.column}:${sort.dir}`
  if (view.offset > 0) q.offset = view.offset
  return q
}

/** The stored part of a form's view: the sort alone, read with parseView's rules. */
function parseSort(stored: unknown): Pick<TableView, 'sort'> | null {
  const sort = parseView(stored)?.sort
  return sort ? { sort } : null
}

/**
 * The submissions table's view. Only the sort is kept in this browser, per
 * form, under `twillingate:forms:<project>:<name>` (`…:archived` for the
 * archived table); the filters and the page
 * live in memory only, since a filter value is usually a person's email.
 * A change of filters or sort returns to the first page.
 */
function useFormView(key: string): [TableView, (next: TableView) => void] {
  const [stored, setStored] = useStoredState(key, parseSort)
  const [filters, setFilters] = useState<Filter[]>([])
  const [offset, setOffset] = useState(0)
  const view: TableView = { filters, sort: stored?.sort ?? null, offset }
  const setView = (next: TableView) => {
    const changed = JSON.stringify([next.filters, next.sort]) !== JSON.stringify([view.filters, view.sort])
    if (JSON.stringify(next.sort) !== JSON.stringify(view.sort)) setStored(next.sort === null ? null : { sort: next.sort })
    setFilters(next.filters)
    setOffset(changed ? 0 : next.offset)
  }
  return [view, setView]
}

/** Which submissions the table shows: the active ones, or the archived ones waiting for the purge. */
type Shown = 'active' | 'archived'

interface SubmissionsProps {
  projectId: number
  form: Form
  shown: Shown
  onShown: (shown: Shown) => void
  /** RETENTION_ARCHIVED_DAYS: null when never purged, undefined while unknown. */
  purgeDays: PurgeDays
}

/**
 * A form's submissions (D12a): the dashboards' table in remote mode, its
 * rows from list_submissions, a row opening the drawer, with **Export CSV**
 * of what the filters match. Checking rows swaps that for **Archive** (or
 * **Restore**), **Export** and **Clear** on just those. The **N archived**
 * link beside it shows the archived submissions instead, with a way back;
 * the daily pass purges them `purgeDays` after archiving.
 */
function Submissions({ projectId, form, shown, onShown, purgeDays }: SubmissionsProps) {
  const name = form.name
  const archived = shown === 'archived'
  const [view, setView] = useFormView(`twillingate:forms:${projectId}:${name}${archived ? ':archived' : ''}`)
  // The last answer: its columns decide which filters are sent, and its
  // rows stay on screen while a new view loads or is refused.
  const [last, setLast] = useState<SubmissionsPage>()
  // The stored view was refused before any answer named the columns: ask
  // once without it, to learn them.
  const [blind, setBlind] = useState(false)
  const args = viewArgs(view, last?.columns ?? (blind ? [] : undefined), archived)
  const q = useQuery({ ...submissionsQuery(projectId, name, args), placeholderData: keepPreviousData })
  const settled = q.isPlaceholderData ? undefined : q.data
  useEffect(() => {
    if (settled) setLast(settled)
  }, [settled])
  const answer = q.data ?? last
  const refused = q.error instanceof ApiError && q.error.status === 400 ? q.error : undefined
  const sentView = Object.keys(args).some((k) => k !== 'archived')
  useEffect(() => {
    if (refused && !last && sentView) setBlind(true)
  }, [refused, last, sentView])
  const viewError = refused && answer ? refused.message : undefined

  const actions = useFormActions()
  const [opened, setOpened] = useState<string>()
  const [downloading, setDownloading] = useState(false)
  const columns = answer?.columns ?? []
  const live = liveFilters(view, columns)
  // The rows checked, by submission id; only those on the page shown count,
  // so a row a filter hides is never archived unseen.
  const [checked, setChecked] = useState<ReadonlySet<string>>(new Set())
  const pageIds = answer?.ids ?? []
  const selected = pageIds.filter((sid) => checked.has(sid))
  const selectedRows = new Set(pageIds.flatMap((sid, i) => (checked.has(sid) ? [i] : [])))

  const fetchDistinct = async (column: string, filters: Filter[]) => {
    const others = liveFilters({ ...view, filters }, columns)
    const res = await endpoints.submissions(projectId, name, {
      ...(archived ? { archived: true } : {}),
      ...(others.length > 0 ? { filters: JSON.stringify(others) } : {}),
      distinct: column,
    })
    const capped = res.matched > res.offset + res.rows.length
    return res.rows.map(([value, n]) => ({ value, rows: Number(n), capped }))
  }

  // Archived ones go back, active ones are set aside; either way the toast says how many.
  const moveSelected = async () => {
    const body = { ids: selected }
    const n = await (archived ? actions.restoreSubmissions(projectId, body) : actions.archiveSubmissions(projectId, body))
    if (n !== undefined) setChecked(new Set())
  }

  // The checked rows as the table shows them, in its order.
  const exportSelected = () => {
    if (!answer) return
    const rows = answer.rows.filter((_, i) => selectedRows.has(i))
    downloadBlob(new Blob([toCSV(answer.columns, rows)], { type: 'text/csv' }), `${name}-selected-submissions.csv`)
  }

  const csv = async () => {
    setDownloading(true)
    try {
      const { filters, sort } = viewArgs({ ...view, offset: 0 }, answer?.columns, archived)
      const blob = await endpoints.exportSubmissions(projectId, name, {
        ...(archived ? { archived: true } : {}),
        ...(filters ? { filters } : {}),
        ...(sort ? { sort } : {}),
      })
      downloadBlob(blob, `${name}-${archived ? 'archived-' : ''}submissions.csv`)
    } catch (err) {
      toast.error(err instanceof Error ? `Couldn't download the CSV: ${err.message}` : "Couldn't download the CSV")
    } finally {
      setDownloading(false)
    }
  }

  const purgeNote = purgeNoteFor(purgeDays)

  return (
    <section aria-label="Submissions" className="flex min-w-0 flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        {archived ? (
          <div className="flex min-w-0 items-center gap-1">
            <Button type="button" variant="ghost" size="sm" className="-ml-2" onClick={() => onShown('active')}>
              <ArrowLeftIcon />
              Submissions
            </Button>
            <h3 className="text-base font-semibold">
              Archived
              {answer && answer.total > 0 && (
              <span className="ml-1 text-sm font-normal text-muted-foreground">
                {' '}
                {live.length > 0
                  ? `${answer.matched.toLocaleString('en-US')} of ${answer.total.toLocaleString('en-US')}`
                  : answer.total.toLocaleString('en-US')}
              </span>
            )}
            </h3>
          </div>
        ) : (
          <h3 className="text-base font-semibold">
            Submissions
            {answer && answer.total > 0 && (
              <span className="ml-1 text-sm font-normal text-muted-foreground">
                {' '}
                {live.length > 0
                  ? `${answer.matched.toLocaleString('en-US')} of ${answer.total.toLocaleString('en-US')}`
                  : answer.total.toLocaleString('en-US')}
              </span>
            )}
          </h3>
        )}
        {selected.length > 0 ? (
          <div role="toolbar" aria-label="Selected submissions" className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">{selected.length.toLocaleString('en-US')} selected</span>
            <Button type="button" variant="outline" size="sm" disabled={actions.pending} onClick={() => void moveSelected()}>
              {archived ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
              {archived ? 'Restore' : 'Archive'}
            </Button>
            <Button type="button" variant="outline" size="sm" onClick={exportSelected}>
              <DownloadIcon />
              Export
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => setChecked(new Set())}>
              <XIcon />
              <span className="max-sm:sr-only">Clear</span>
            </Button>
          </div>
        ) : (
          <div className="flex flex-wrap items-center gap-2">
            {!archived && form.archived_submissions > 0 && (
              <Button type="button" variant="link" size="sm" className="text-muted-foreground" onClick={() => onShown('archived')}>
                {form.archived_submissions.toLocaleString('en-US')} archived
              </Button>
            )}
            <Button type="button" variant="outline" size="sm" disabled={downloading || !answer} onClick={() => void csv()}>
              <DownloadIcon />
              Export CSV
            </Button>
          </div>
        )}
      </div>
      {archived && <p className="text-xs text-muted-foreground">{purgeNote}</p>}
      {!answer ? (
        q.error ? (
          <LoadError what="the submissions" error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <Skeleton aria-hidden className="h-40 w-full" />
        )
      ) : answer.total === 0 ? (
        <div className="flex flex-col items-center gap-1.5 rounded-lg border p-6 text-center text-muted-foreground [&>svg]:size-5">
          {archived ? <ArchiveIcon /> : <InboxIcon />}
          <p className="text-sm">{archived ? 'No archived submissions.' : 'No submissions yet.'}</p>
        </div>
      ) : (
        <div className="flex max-h-[75vh] min-h-0 flex-col">
          <WidgetFrame>
            <Table
              data={{ columns: answer.columns, rows: answer.rows, truncated: false }}
              props={{ mode: 'remote' }}
              view={view}
              onView={setView}
              fetchDistinct={fetchDistinct}
              page={{ offset: answer.offset, limit: answer.limit, matched: answer.matched, total: answer.total }}
              viewError={viewError}
              reloading={q.isPlaceholderData || (q.isFetching && q.data !== undefined)}
              onRow={(i) => setOpened(answer.ids[i])}
              selection={{ selected: selectedRows, onChange: (rows) => setChecked(new Set([...rows].map((i) => answer.ids[i]))) }}
            />
          </WidgetFrame>
        </div>
      )}
      <SubmissionDrawer projectId={projectId} form={name} id={opened} purgeDays={purgeDays} onClose={() => setOpened(undefined)} />
    </section>
  )
}

/**
 * `/projects/:id/forms/:name` (D12), inside the Forms tab: a crumb back to
 * the list, a draft's banner with **Approve**, the submissions table, and
 * the form's menu, whose **Settings…** opens its settings dialog.
 */
export default function FormPage({ project, name }: { project: Project; name: string }) {
  const id = project.project_id
  const q = useQuery(formsQuery(id))
  const [url] = useSearchParams()
  const back = tabPath(id, FORMS_ID, rangeParams(url).toString())
  const actions = useFormActions()
  const [approving, setApproving] = useState(false)
  const [setting, setSetting] = useState(false)
  const navigate = useNavigate()
  // An archived form leaves this page for the list it left.
  const archive = async () => {
    if (await actions.archive(id, name)) void navigate(back)
  }
  const [shown, setShown] = useState<Shown>('active')
  const purgeDays = purgeDaysOf(useQuery(dashboardsQuery).data)
  const form = q.data?.forms.find((f) => f.name === name)

  // Opening a form reads it: up to the last submission the list showed, so
  // one that arrives meanwhile stays new. Once per value, not per refetch.
  const markSeen = useMarkFormSeen()
  const until = form?.last_submitted_at
  const unseen = form !== undefined && form.new_submissions > 0
  useEffect(() => {
    if (until && unseen) markSeen(id, name, until)
  }, [id, name, until, unseen, markSeen])

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <Link to={back} className="inline-flex items-center gap-1 self-start text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeftIcon className="size-4" />
        Forms
      </Link>
      {q.error && !q.data ? (
        <LoadError what="the form" error={q.error} onRetry={() => void q.refetch()} />
      ) : !q.data ? (
        <Skeleton aria-hidden className="h-24 w-full" />
      ) : !form ? (
        <p className="text-sm break-words text-muted-foreground">No form {name} among this project's active forms; an archived one is on the Archive page.</p>
      ) : (
        <>
          <header className="flex min-w-0 flex-wrap items-center gap-2">
            <h2 className="font-mono text-lg font-semibold break-all">{form.name}</h2>
            <Badge variant={form.status === 'draft' ? 'outline' : 'secondary'}>{formStatus(form)}</Badge>
            <div className="ml-auto">
              <FormMenu
                form={form}
                pending={actions.pending}
                onSettings={() => setSetting(true)}
                onApprove={() => setApproving(true)}
                onClosing={(closes_at) => actions.update(id, name, { closes_at }, closingDone(name, closes_at))}
                onArchive={() => void archive()}
              />
            </div>
          </header>
          {form.status === 'draft' && (
            <div role="status" className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-dashed p-3">
              <p className="text-sm">
                Draft: accepting until {formatDay(new Date(form.draft_until ?? form.created_at))}, then archived
              </p>
              <Button type="button" size="sm" disabled={actions.pending} onClick={() => setApproving(true)}>
                Approve
              </Button>
            </div>
          )}
          {/* Keyed by what it shows: each table keeps its own view and last answer. */}
          <Submissions key={shown} projectId={id} form={form} shown={shown} onShown={setShown} purgeDays={purgeDays} />
          <SettingsDialog
            open={setting}
            onOpenChange={setSetting}
            form={form}
            pending={actions.pending}
            onSave={(body) => actions.update(id, name, body)}
          />
          <ApproveDialog
            open={approving}
            onOpenChange={setApproving}
            name={name}
            fields={form.fields}
            pending={actions.pending}
            onApprove={(fields) => actions.approve(id, name, fields)}
          />
        </>
      )}
    </div>
  )
}

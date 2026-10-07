import { useEffect, useId, useState, type FormEvent } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { ArrowLeftIcon, DownloadIcon, InboxIcon, Trash2Icon } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import WidgetFrame from '@/components/WidgetFrame'
import LoadError from '@/components/projects/LoadError'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import Table from '@/components/widgets/table'
import { useFormActions, type FormActions } from '@/hooks/use-form-actions'
import { useStoredState } from '@/hooks/use-stored-state'
import { ApiError, endpoints, type Form, type Project, type SubmissionsPage, type SubmissionsQuery } from '@/lib/api'
import { downloadBlob } from '@/lib/capture'
import { formatDay, formatDayTime, formStatus } from '@/lib/forms'
import { FORMS_ID, rangeParams, tabPath } from '@/lib/project-tabs'
import { formsQuery, submissionsQuery } from '@/lib/queries'
import { liveFilters, parseView, type Filter, type TableView } from '@/lib/table-view'
import ApproveDialog, { FieldPicker } from './ApproveDialog'
import ClosingControl from './ClosingControl'
import SubmissionDrawer from './SubmissionDrawer'

/**
 * The table's arguments for a view: once an answer has named the columns,
 * only the filters and sort on columns it has (the server refuses the
 * rest); before one has, the view whole. Empty parts are left out.
 */
function viewArgs(view: TableView, columns: string[] | undefined): SubmissionsQuery {
  const filters = columns ? liveFilters(view, columns) : view.filters
  const sort = view.sort && (!columns || columns.includes(view.sort.column)) ? view.sort : null
  const q: SubmissionsQuery = {}
  if (filters.length > 0) q.filters = JSON.stringify(filters)
  if (sort) q.sort = `${sort.column}:${sort.dir}`
  if (view.offset > 0) q.offset = view.offset
  return q
}

/**
 * The submissions table's view: filters and sort kept in this browser per
 * form, under `twillingate:forms:<project>:<name>`; the page in memory only.
 * A change of filters or sort returns to the first page.
 */
function useFormView(key: string): [TableView, (next: TableView) => void] {
  const [stored, setStored] = useStoredState(key, parseView)
  const [offset, setOffset] = useState(0)
  const view: TableView = { filters: stored?.filters ?? [], sort: stored?.sort ?? null, offset }
  const setView = (next: TableView) => {
    const changed = JSON.stringify([next.filters, next.sort]) !== JSON.stringify([view.filters, view.sort])
    if (changed) setStored(next.filters.length === 0 && next.sort === null ? null : { ...next, offset: 0 })
    setOffset(changed ? 0 : next.offset)
  }
  return [view, setView]
}

function plural(n: number, one: string): string {
  return `${n.toLocaleString('en-US')} ${n === 1 ? one : `${one}s`}`
}

/**
 * A form's submissions (D12a): the dashboards' table in remote mode, its
 * rows from list_submissions, a row opening the drawer, with **Delete all
 * matching** (only with filters set) and **CSV** of what the filters match.
 */
function Submissions({ projectId, form }: { projectId: number; form: Form }) {
  const name = form.name
  const [view, setView] = useFormView(`twillingate:forms:${projectId}:${name}`)
  // The last answer: its columns decide which filters are sent, and its
  // rows stay on screen while a new view loads or is refused.
  const [last, setLast] = useState<SubmissionsPage>()
  // The stored view was refused before any answer named the columns: ask
  // once without it, to learn them.
  const [blind, setBlind] = useState(false)
  const args = viewArgs(view, last?.columns ?? (blind ? [] : undefined))
  const q = useQuery({ ...submissionsQuery(projectId, name, args), placeholderData: keepPreviousData })
  const settled = q.isPlaceholderData ? undefined : q.data
  useEffect(() => {
    if (settled) setLast(settled)
  }, [settled])
  const answer = q.data ?? last
  const refused = q.error instanceof ApiError && q.error.status === 400 ? q.error : undefined
  const sentView = Object.keys(args).length > 0
  useEffect(() => {
    if (refused && !last && sentView) setBlind(true)
  }, [refused, last, sentView])
  const viewError = refused && answer ? refused.message : undefined

  const actions = useFormActions()
  const [opened, setOpened] = useState<string>()
  const [confirming, setConfirming] = useState(false)
  const [downloading, setDownloading] = useState(false)
  const columns = answer?.columns ?? []
  const live = liveFilters(view, columns)
  const matched = answer?.matched ?? 0
  // While a new view loads, the count on screen is the old view's: deleting
  // then would confirm one number and send another view's filters.
  const deletable = !actions.pending && !q.isPlaceholderData && live.length > 0 && matched > 0

  const fetchDistinct = async (column: string, filters: Filter[]) => {
    const others = liveFilters({ ...view, filters }, columns)
    const res = await endpoints.submissions(projectId, name, {
      ...(others.length > 0 ? { filters: JSON.stringify(others) } : {}),
      distinct: column,
    })
    const capped = res.matched > res.offset + res.rows.length
    return res.rows.map(([value, n]) => ({ value, rows: Number(n), capped }))
  }

  const deleteMatching = async () => {
    await actions.deleteSubmissions(projectId, { form: name, filters: JSON.stringify(live) })
  }

  const csv = async () => {
    setDownloading(true)
    try {
      const { filters, sort } = viewArgs({ ...view, offset: 0 }, answer?.columns)
      const blob = await endpoints.exportSubmissions(projectId, name, { ...(filters ? { filters } : {}), ...(sort ? { sort } : {}) })
      downloadBlob(blob, `${name}-submissions.csv`)
    } catch (err) {
      toast.error(err instanceof Error ? `Couldn't download the CSV: ${err.message}` : "Couldn't download the CSV")
    } finally {
      setDownloading(false)
    }
  }

  return (
    <section aria-label="Submissions" className="flex min-w-0 flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-base font-semibold">
          Submissions
          {answer && answer.total > 0 && (
            <span className="ml-2 text-sm font-normal text-muted-foreground">
              {live.length > 0 ? `${answer.matched.toLocaleString('en-US')} of ${answer.total.toLocaleString('en-US')}` : answer.total.toLocaleString('en-US')}
            </span>
          )}
        </h3>
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={!deletable}
            onClick={() => setConfirming(true)}
          >
            <Trash2Icon />
            Delete all matching
          </Button>
          <Button type="button" variant="outline" size="sm" disabled={downloading || !answer} onClick={() => void csv()}>
            <DownloadIcon />
            CSV
          </Button>
        </div>
      </div>
      {!answer ? (
        q.error ? (
          <LoadError what="the submissions" error={q.error} onRetry={() => void q.refetch()} />
        ) : (
          <Skeleton aria-hidden className="h-40 w-full" />
        )
      ) : answer.total === 0 ? (
        <div className="flex flex-col items-center gap-1.5 rounded-lg border p-6 text-center text-muted-foreground [&>svg]:size-5">
          <InboxIcon />
          <p className="text-sm">No submissions yet.</p>
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
            />
          </WidgetFrame>
        </div>
      )}
      <SubmissionDrawer projectId={projectId} form={name} id={opened} onClose={() => setOpened(undefined)} />
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {plural(matched, 'matching submission')}?</AlertDialogTitle>
            <AlertDialogDescription>
              Every submission these filters match is deleted for good, with its conversion while that is still in the raw window.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction disabled={!deletable} onClick={() => void deleteMatching()}>
              Delete submissions
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

/** Purpose and return URL, saved together; only what changed is sent. */
function Details({ projectId, form, actions }: { projectId: number; form: Form; actions: FormActions }) {
  const id = useId()
  const [purpose, setPurpose] = useState(form.purpose)
  const [returnURL, setReturnURL] = useState(form.return_url)
  // Follow what is saved, from here or elsewhere.
  useEffect(() => setPurpose(form.purpose), [form.purpose])
  useEffect(() => setReturnURL(form.return_url), [form.return_url])
  const changed = purpose.trim() !== form.purpose || returnURL.trim() !== form.return_url
  const save = (e: FormEvent) => {
    e.preventDefault()
    const body: { purpose?: string; return_url?: string } = {}
    if (purpose.trim() !== form.purpose) body.purpose = purpose.trim()
    if (returnURL.trim() !== form.return_url) body.return_url = returnURL.trim()
    void actions.update(projectId, form.name, body)
  }
  return (
    <form onSubmit={save} className="flex flex-col gap-3">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-purpose`}>Purpose</Label>
        <Input id={`${id}-purpose`} value={purpose} onChange={(e) => setPurpose(e.target.value)} placeholder="What the form is for" />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={`${id}-return`}>Return URL</Label>
        <Input
          id={`${id}-return`}
          type="url"
          value={returnURL}
          onChange={(e) => setReturnURL(e.target.value)}
          placeholder="https://example.com/thanks"
        />
        <p className="text-xs text-muted-foreground">
          Where a plain HTML form sends the visitor back when it sets no <code className="font-mono">$redirect</code>; its origin must be
          one of the project's allowed origins.
        </p>
      </div>
      <Button type="submit" variant="outline" className="self-start" disabled={actions.pending || !changed}>
        Save
      </Button>
    </form>
  )
}

/** An approved form's expected fields: change which are kept, never none. */
function ExpectedFields({ projectId, form, actions }: { projectId: number; form: Form; actions: FormActions }) {
  const id = useId()
  const expected = form.expected_fields ?? []
  const [checked, setChecked] = useState(expected)
  const saved = JSON.stringify(expected)
  useEffect(() => setChecked(JSON.parse(saved) as string[]), [saved])
  const same = JSON.stringify(checked) === saved
  return (
    <div role="group" aria-labelledby={`${id}-title`} className="flex flex-col gap-2">
      <h3 id={`${id}-title`} className="text-sm font-medium">
        Expected fields
      </h3>
      <p className="text-xs text-muted-foreground">New submissions keep only these, as the table's columns in this order.</p>
      <FieldPicker fields={form.fields} kept={expected} checked={checked} onChange={setChecked} disabled={actions.pending} />
      <Button
        type="button"
        variant="outline"
        className="self-start"
        disabled={actions.pending || same || checked.length === 0}
        onClick={() => void actions.update(projectId, form.name, { expected_fields: checked })}
      >
        Save fields
      </Button>
    </div>
  )
}

/**
 * `/projects/:id/forms/:name` (D12), inside the Forms tab: a crumb back to
 * the list, a draft's banner with **Approve**, the submissions table, and
 * the form's settings: purpose and return URL, an approved form's expected
 * fields, and its closing.
 */
export default function FormPage({ project, name }: { project: Project; name: string }) {
  const id = project.project_id
  const q = useQuery(formsQuery(id))
  const [url] = useSearchParams()
  const back = tabPath(id, FORMS_ID, rangeParams(url).toString())
  const actions = useFormActions()
  const [approving, setApproving] = useState(false)
  const form = q.data?.forms.find((f) => f.name === name)

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
          <Submissions projectId={id} form={form} />
          <section aria-label="Settings" className="flex flex-col gap-5 rounded-lg border p-4">
            <h3 className="text-base font-semibold">Settings</h3>
            <Details projectId={id} form={form} actions={actions} />
            {form.status === 'approved' && <ExpectedFields projectId={id} form={form} actions={actions} />}
            <ClosingControl
              closesAt={form.closes_at}
              pending={actions.pending}
              onChange={(closes_at) =>
                actions.update(
                  id,
                  name,
                  { closes_at },
                  closes_at === null
                    ? `Reopened ${name}`
                    : Date.parse(closes_at) <= Date.now()
                      ? `Closed ${name}`
                      : `${name} closes ${formatDayTime(new Date(closes_at))}`
                )
              }
            />
          </section>
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

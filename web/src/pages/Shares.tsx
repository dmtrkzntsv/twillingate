import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import CopyButton from '@/components/projects/CopyButton'
import LoadError from '@/components/projects/LoadError'
import { ArchiveAfterSelect } from '@/components/share/ArchiveAfterSelect'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useWidgetShareActions } from '@/hooks/use-widget-share-actions'
import type { WidgetShare } from '@/lib/api'
import { dashboardsQuery, widgetSharesQuery } from '@/lib/queries'
import { archiveLabel, embedCode, rangeInWords } from '@/lib/share'

/** The day a share was made, "Oct 5, 2026", in UTC like the archive date beside it. */
function createdOn(share: WidgetShare): string {
  return new Date(share.created_at).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' })
}

/** `?widget=` as a widget id, or undefined when it is absent or not one. */
function widgetFilter(param: string | null): number | undefined {
  const id = Number(param)
  return param && Number.isSafeInteger(id) && id > 0 ? id : undefined
}

/**
 * `/shares`: the live links to shared widgets, newest first, each with its
 * thumbnail, the widget it came from, and what it can still do: change when
 * it archives, copy its link or embed code, archive now. `?widget=<id>`
 * narrows it to one widget (where the Share dialog's "other links" leads).
 * Archiving asks no confirmation, since Restore on the Archive page undoes
 * it. Below `sm` range, created and archive-after fold into a line under
 * the widget.
 */
export default function Shares() {
  const { data: dash } = useQuery(dashboardsQuery)
  const [params, setParams] = useSearchParams()
  const widgetId = widgetFilter(params.get('widget'))
  const sharesQ = useQuery(widgetSharesQuery({ ...(widgetId ? { widget_id: widgetId } : {}), state: 'live' }))
  const { setArchiveAfter, archive, pending } = useWidgetShareActions()
  const shares = sharesQ.data?.shares

  const row = (s: WidgetShare) => {
    const archives = s.archive_at ? archiveLabel(s) : undefined
    return (
      <TableRow key={s.id}>
        <TableCell className="w-28">
          <a href={s.url} target="_blank" rel="noopener" className="block">
            <img src={s.image_url} alt={`Shared image of ${s.title}`} loading="lazy" className="aspect-[1200/630] w-24 rounded border object-cover" />
          </a>
        </TableCell>
        <TableCell className="min-w-0">
          <div className="truncate font-medium">{s.title}</div>
          <div className="truncate text-xs text-muted-foreground">
            {s.dashboard_id !== null && s.dashboard_title ? (
              <>
                <Link to={`/dashboards/${s.dashboard_id}`} className="underline underline-offset-2">
                  {s.dashboard_title}
                </Link>
                {' · '}
              </>
            ) : null}
            {s.project_name}
          </div>
          <div className="mt-1 text-xs whitespace-normal text-muted-foreground sm:hidden">
            {rangeInWords(s.from, s.to)} · {createdOn(s)} · {archives ? `archives ${archives}` : archiveLabel(s)}
          </div>
        </TableCell>
        <TableCell className="hidden sm:table-cell">{rangeInWords(s.from, s.to)}</TableCell>
        <TableCell className="hidden sm:table-cell">{createdOn(s)}</TableCell>
        <TableCell className="hidden sm:table-cell">
          <ArchiveAfterSelect
            aria-label={`Archive ${s.title} after`}
            value="project"
            current={archives}
            onChange={(v) => void setArchiveAfter(s.id, v)}
          />
          {archives && <div className="mt-1 text-xs text-muted-foreground">archives {archives}</div>}
        </TableCell>
        <TableCell>
          <div className="flex flex-wrap justify-end gap-1">
            <CopyButton value={s.url} label="Copy link" />
            <CopyButton value={embedCode(s)} label="Copy embed code" />
            <Button variant="outline" size="sm" disabled={pending} onClick={() => void archive(s.id)}>
              Archive
            </Button>
          </div>
        </TableCell>
      </TableRow>
    )
  }

  return (
    <AppShell dashboards={dash?.dashboards ?? []} currentId={0} readOnly={dash?.dev === true}>
      <TopBar>
        <Crumbs items={[{ label: 'Shares' }]} />
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        <header className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold tracking-tight">Shares</h1>
          <p className="max-w-prose text-sm text-muted-foreground">Links to shared widgets. Anyone with a link sees its image; archive a link to take it down.</p>
          {widgetId && (
            <Button variant="outline" size="sm" className="self-start" title="Show every share" onClick={() => setParams({})}>
              Widget {widgetId} ×
            </Button>
          )}
        </header>
        {sharesQ.isError && !shares && <LoadError what="shares" error={sharesQ.error} onRetry={() => void sharesQ.refetch()} />}
        {shares && shares.length === 0 && (
          <p className="text-sm text-muted-foreground">
            {widgetId ? 'This widget has no live links.' : "No shared widgets. Share one from a widget's menu: Share…"}
          </p>
        )}
        {shares && shares.length > 0 && (
          <div className="min-w-0">
            <Table className="table-fixed">
              <TableHeader>
                <TableRow>
                  <TableHead className="w-28">
                    <span className="sr-only">Image</span>
                  </TableHead>
                  <TableHead>Widget</TableHead>
                  <TableHead className="hidden w-40 sm:table-cell">Range</TableHead>
                  <TableHead className="hidden w-28 sm:table-cell">Created</TableHead>
                  <TableHead className="hidden w-40 sm:table-cell">Archive after</TableHead>
                  <TableHead className="w-24 text-right sm:w-44">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>{shares.map(row)}</TableBody>
            </Table>
          </div>
        )}
      </div>
    </AppShell>
  )
}

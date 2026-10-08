import { useQuery } from '@tanstack/react-query'
import { ArchiveIcon, CodeIcon, ExternalLinkIcon, LinkIcon, MoreHorizontalIcon } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import CopyButton from '@/components/projects/CopyButton'
import LoadError from '@/components/projects/LoadError'
import { ArchiveAfterMenu } from '@/components/share/ArchiveAfterMenu'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCopy } from '@/hooks/use-copy'
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
 * thumbnail, the widget it came from, its range (the day it was made on
 * hover), when it archives, a copy of its link, and a menu for the rest:
 * the embed code, Open, Archive. `?widget=<id>` narrows it to one widget
 * (where the Share dialog's "other links" leads). Archiving asks no
 * confirmation, since Restore on the Archive page undoes it. Below `lg` the
 * range and archive date fold into a line under the widget.
 */
export default function Shares() {
  const { data: dash } = useQuery(dashboardsQuery)
  const [params, setParams] = useSearchParams()
  const widgetId = widgetFilter(params.get('widget'))
  const sharesQ = useQuery(widgetSharesQuery({ ...(widgetId ? { widget_id: widgetId } : {}), state: 'live' }))
  const { setArchiveAfter, archive, pending } = useWidgetShareActions()
  const { copy } = useCopy()
  const shares = sharesQ.data?.shares

  // The menu closes as it copies, so a toast says how it went.
  const copyEmbed = (s: WidgetShare) =>
    void copy(embedCode(s)).then((out) => (out === 'copied' ? toast.success('Embed code copied') : toast.error("Couldn't copy the embed code")))

  const range = (s: WidgetShare) => <span title={`Created ${createdOn(s)}`}>{rangeInWords(s.from, s.to)}</span>
  const archiveDate = (s: WidgetShare, small: boolean) => (
    <ArchiveAfterMenu current={archiveLabel(s)} small={small} disabled={pending} onChange={(v) => void setArchiveAfter(s.id, v)} />
  )

  const row = (s: WidgetShare) => {
    const from = [s.dashboard_title, s.project_name].filter(Boolean).join(' · ')
    return (
      <TableRow key={s.id}>
        <TableCell className="w-24 sm:w-28">
          <a href={s.url} target="_blank" rel="noopener" className="block">
            <img src={s.image_url} alt={`Shared image of ${s.title}`} loading="lazy" className="aspect-[1200/630] w-20 rounded border object-cover sm:w-24" />
          </a>
        </TableCell>
        <TableCell className="min-w-0">
          <div className="truncate font-medium" title={s.title}>
            {s.title}
          </div>
          <div className="truncate text-xs text-muted-foreground" title={from}>
            {s.dashboard_id !== null && s.dashboard_title ? (
              <>
                <Link to={`/dashboards/${s.dashboard_id}`} className="underline underline-offset-2">
                  {s.dashboard_title}
                </Link>
                {' · '}
                {s.project_name}
              </>
            ) : (
              from
            )}
          </div>
          {/* Below lg the persistent sidebar leaves no room for the two columns: they fold into this line. */}
          <div className="flex flex-col items-start text-xs whitespace-normal text-muted-foreground lg:hidden">
            {range(s)}
            {/* With no column header to say what the date is. */}
            <span className="flex items-center">archives {archiveDate(s, true)}</span>
          </div>
        </TableCell>
        <TableCell className="hidden whitespace-normal lg:table-cell">{range(s)}</TableCell>
        <TableCell className="hidden lg:table-cell">{archiveDate(s, false)}</TableCell>
        <TableCell>
          <div className="flex justify-end gap-1">
            <CopyButton value={s.url} label="Copy link" icon={<LinkIcon />} />
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" aria-label={`Actions for ${s.title}`}>
                  <MoreHorizontalIcon />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={() => copyEmbed(s)}>
                  <CodeIcon />
                  Copy embed code
                </DropdownMenuItem>
                <DropdownMenuItem asChild>
                  <a href={s.url} target="_blank" rel="noopener">
                    <ExternalLinkIcon />
                    Open
                  </a>
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem disabled={pending} onClick={() => void archive(s.id)}>
                  <ArchiveIcon />
                  Archive
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
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
                  <TableHead className="w-24 sm:w-28">
                    <span className="sr-only">Image</span>
                  </TableHead>
                  <TableHead>Widget</TableHead>
                  <TableHead className="hidden w-44 lg:table-cell">Range</TableHead>
                  <TableHead className="hidden w-40 lg:table-cell">Archives</TableHead>
                  <TableHead className="w-24">
                    <span className="sr-only">Actions</span>
                  </TableHead>
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

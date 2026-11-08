import { useState } from 'react'
import { useInfiniteQuery, useQuery, useQueryClient, type InfiniteData } from '@tanstack/react-query'
import {
  ArchiveIcon,
  ArchiveRestoreIcon,
  BellIcon,
  CircleCheckIcon,
  ClockAlertIcon,
  FilePlusIcon,
  FileTextIcon,
  FileXIcon,
  GlobeIcon,
  ServerCrashIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  type LucideIcon,
} from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import ConfirmDialog from '@/components/ConfirmDialog'
import Crumbs from '@/components/Crumbs'
import LoadError from '@/components/projects/LoadError'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { endpoints, type Notification, type NotificationsPage, type NotificationType } from '@/lib/api'
import { dashboardsQuery, notificationsQuery } from '@/lib/queries'
import { toastError } from '@/lib/toast-error'
import { formatAgo } from '@/lib/units'
import { cn } from '@/lib/utils'

const FAILING = 'text-destructive'
const RECOVERED = 'text-emerald-600 dark:text-emerald-400'
const EXPIRING = 'text-amber-600 dark:text-amber-400'
const NEUTRAL = 'text-muted-foreground'

/** Each type's icon and tone: failing red, recovered green, expiring amber, forms neutral. */
const LOOK: Record<NotificationType, { icon: LucideIcon; tone: string }> = {
  uptime_down: { icon: ServerCrashIcon, tone: FAILING },
  uptime_still_down: { icon: ServerCrashIcon, tone: FAILING },
  uptime_up: { icon: CircleCheckIcon, tone: RECOVERED },
  cert_failing: { icon: ShieldAlertIcon, tone: FAILING },
  cert_still_failing: { icon: ShieldAlertIcon, tone: FAILING },
  cert_recovered: { icon: ShieldCheckIcon, tone: RECOVERED },
  domain_failing: { icon: GlobeIcon, tone: FAILING },
  domain_still_failing: { icon: GlobeIcon, tone: FAILING },
  domain_recovered: { icon: GlobeIcon, tone: RECOVERED },
  cert_expiring: { icon: ClockAlertIcon, tone: EXPIRING },
  domain_expiring: { icon: ClockAlertIcon, tone: EXPIRING },
  form_draft_created: { icon: FilePlusIcon, tone: NEUTRAL },
  form_draft_discarded: { icon: FileXIcon, tone: NEUTRAL },
  form_submission: { icon: FileTextIcon, tone: NEUTRAL },
}

/** A type this console does not know yet (a newer server's) still shows, plainly. */
const UNKNOWN = { icon: BellIcon, tone: NEUTRAL }

/** When it arrived, in full and in the browser's own time, for the relative time's hover. */
function fullTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

function Item({ n, archived, pending, onMove }: { n: Notification; archived: boolean; pending: boolean; onMove: () => void }) {
  const { icon: Icon, tone } = LOOK[n.type] ?? UNKNOWN
  return (
    <li aria-label={n.title} className="flex min-w-0 gap-3 border-b py-3 last:border-b-0">
      <Icon aria-hidden className={cn('mt-0.5 size-4 shrink-0', tone)} />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <p className="font-medium break-words">{n.title}</p>
        {n.body && <p className="text-sm break-words whitespace-pre-line text-muted-foreground">{n.body}</p>}
        <p className="text-xs break-words text-muted-foreground">
          {n.project && `${n.project} · `}
          <time dateTime={n.created_at} title={fullTime(n.created_at)}>
            {formatAgo(n.created_at)}
          </time>
        </p>
        {n.delivery === 'failed' && (
          <p className="text-xs break-words text-muted-foreground">Not delivered: {n.delivery_error || 'the channel refused it'}</p>
        )}
        <div className="mt-1 flex flex-wrap gap-2">
          {/* An item about nothing else leads here: no link to the page it is on. */}
          {n.path && n.path !== '/inbox' && (
            <Button asChild variant="outline" size="sm">
              <Link to={n.path}>Open</Link>
            </Button>
          )}
          <Button type="button" variant="ghost" size="sm" disabled={pending} onClick={onMove}>
            {archived ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
            {archived ? 'Restore' : 'Archive'}
          </Button>
        </div>
      </div>
    </li>
  )
}

/**
 * `/inbox`: what twillingate noticed, newest first, each item the message
 * the channel got, with a link to the page it is about. Two tabs, Active
 * and Archived (`?show=archived`); an item archives (or restores) with one
 * click, and Active has Archive all behind a confirmation. Any of these
 * refetches every query under `['notifications']`, the sidebar's count
 * with them. Pages of 50, the next one on Load more.
 */
export default function Inbox() {
  const { data: dash } = useQuery(dashboardsQuery)
  const [params, setParams] = useSearchParams()
  const archived = params.get('show') === 'archived'
  const query = notificationsQuery(archived)
  const listQ = useInfiniteQuery(query)
  const client = useQueryClient()
  const [pending, setPending] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const items = listQ.data?.pages.flatMap((p) => p.notifications)

  /** Runs an archive or a restore; on success drops `ids` (every row when absent) from the list at once, then refetches. */
  const move = async (send: () => Promise<unknown>, ids?: number[]) => {
    setPending(true)
    try {
      await send()
      client.setQueryData<InfiniteData<NotificationsPage, string>>(query.queryKey, (data) =>
        data && {
          ...data,
          pages: data.pages.map((p) => ({ ...p, notifications: ids ? p.notifications.filter((n) => !ids.includes(n.id)) : [] })),
        }
      )
      await client.invalidateQueries({ queryKey: ['notifications'] })
    } catch (err) {
      toastError(err)
    } finally {
      setPending(false)
    }
  }
  const moveOne = (n: Notification) =>
    void move(() => (archived ? endpoints.restoreNotifications([n.id]) : endpoints.archiveNotifications({ ids: [n.id] })), [n.id])

  return (
    <AppShell dashboards={dash?.dashboards ?? []} currentId={0}>
      <TopBar>
        <Crumbs items={[{ label: 'Inbox' }]} />
      </TopBar>
      <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        <header className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold tracking-tight">Inbox</h1>
          <p className="max-w-prose text-sm text-muted-foreground">
            Sites down and back up, certificates and domains failing or about to expire, and forms. Archive an item once it is handled.
          </p>
        </header>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Tabs value={archived ? 'archived' : 'active'} onValueChange={(v) => setParams(v === 'archived' ? { show: 'archived' } : {})}>
            <TabsList aria-label="Notifications shown">
              <TabsTrigger value="active">Active</TabsTrigger>
              <TabsTrigger value="archived">Archived</TabsTrigger>
            </TabsList>
          </Tabs>
          {!archived && (
            <Button type="button" variant="outline" size="sm" disabled={pending || !items || items.length === 0} onClick={() => setConfirming(true)}>
              <ArchiveIcon />
              Archive all
            </Button>
          )}
        </div>
        {listQ.isError && !items && <LoadError what="the inbox" error={listQ.error} onRetry={() => void listQ.refetch()} />}
        {items && items.length === 0 && (
          <p className="text-sm text-muted-foreground">{archived ? 'Nothing archived' : 'Nothing in your inbox'}</p>
        )}
        {items && items.length > 0 && (
          <ul aria-label="Notifications" className="flex min-w-0 flex-col">
            {items.map((n) => (
              <Item key={n.id} n={n} archived={archived} pending={pending} onMove={() => moveOne(n)} />
            ))}
          </ul>
        )}
        {listQ.hasNextPage && (
          <Button type="button" variant="outline" size="sm" className="self-start" disabled={listQ.isFetchingNextPage} onClick={() => void listQ.fetchNextPage()}>
            Load more
          </Button>
        )}
      </div>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Archive every item?"
        description="Every active item, of every project, moves to Archived, where you can restore it until it is purged."
        action="Archive all"
        onAction={() => void move(() => endpoints.archiveNotifications({ all: true }))}
      />
    </AppShell>
  )
}

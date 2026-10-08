import type { ReactNode } from 'react'
import { FolderPlusIcon, LayoutGridIcon, TriangleAlertIcon } from 'lucide-react'
import { Link } from 'react-router'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { ApiError } from '@/lib/api'
import { TopBar } from './AppShell'
import NotFound from './NotFound'

// What a dashboard page shows instead of (or above) its grid.

export function Notice({ icon, title, children }: { icon: ReactNode; title: string; children?: ReactNode }) {
  return (
    <Empty className="border md:p-10">
      <EmptyHeader>
        <EmptyMedia variant="icon">{icon}</EmptyMedia>
        <EmptyTitle className="text-base">{title}</EmptyTitle>
        {children && <EmptyDescription>{children}</EmptyDescription>}
      </EmptyHeader>
    </Empty>
  )
}

export function NoProjects() {
  return (
    <Notice icon={<FolderPlusIcon />} title="Create a project first">
      This dashboard shows one project at a time, and there are no active projects. Ask your agent to call{' '}
      <code className="font-mono text-foreground">create_project</code>, or run{' '}
      <code className="font-mono text-foreground">twillingate project create -name "My App"</code>.
    </Notice>
  )
}

export function NoWidgets() {
  return <Notice icon={<LayoutGridIcon />} title="No widgets yet — ask your agent to add some" />
}

/** `reporting dev`: the dashboard directories that failed to load. */
export function DevErrors({ errors }: { errors: { dir: string; message: string }[] }) {
  return (
    <Alert variant="destructive">
      <TriangleAlertIcon />
      <AlertTitle>Some dashboards did not load</AlertTitle>
      <AlertDescription>
        <ul className="list-disc pl-4">
          {errors.map((e) => (
            <li key={e.dir}>
              <span className="font-mono">{e.dir}</span>: {e.message}
            </li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  )
}

/** A failed load: the not-found page for a 404, else the error with a retry. `bare` leaves out the page chrome. */
export function PageError({ error, onRetry, bare }: { error: Error; onRetry: () => void; bare?: boolean }) {
  // A 401 is already on its way to the login page.
  if (error instanceof ApiError && error.status === 401) return null
  const body =
    error instanceof ApiError && error.status === 404 ? (
      <NoSuchDashboard inPage={bare} />
    ) : (
      <Notice icon={<TriangleAlertIcon />} title="Couldn't load this dashboard">
        <span className="flex flex-col items-center gap-3">
          {error.message}
          <Button variant="outline" size="sm" onClick={onRetry}>
            Retry
          </Button>
        </span>
      </Notice>
    )
  if (bare) return body
  return (
    <>
      <TopBar />
      <div className="p-3 sm:p-4 lg:p-6">{body}</div>
    </>
  )
}

function NoSuchDashboard({ inPage }: { inPage?: boolean }) {
  return (
    <NotFound
      title="No dashboard at this address"
      inPage={inPage}
      actions={
        <Button asChild variant="outline">
          <Link to="/dashboards">Go to your dashboards</Link>
        </Button>
      }
    >
      It may have been deleted, or the link may be wrong.
    </NotFound>
  )
}

export function GridSkeleton() {
  return (
    <div className="grid auto-rows-[40px] grid-cols-12 gap-3" aria-busy="true">
      {[0, 1, 2, 3].map((i) => (
        <Skeleton key={i} className="col-span-6 row-span-3 lg:col-span-3" />
      ))}
      <Skeleton className="col-span-12 row-span-8 lg:col-span-6" />
      <Skeleton className="col-span-12 row-span-8 lg:col-span-6" />
    </div>
  )
}

export function PageLoading() {
  return (
    <>
      <TopBar>
        <Skeleton className="h-5 w-48" />
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-col gap-4 p-3 sm:p-4 lg:p-6">
        <Skeleton className="h-8 w-40" />
        <GridSkeleton />
      </div>
    </>
  )
}

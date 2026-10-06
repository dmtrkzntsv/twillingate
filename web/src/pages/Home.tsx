import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router'
import StatusCard from '@/components/StatusCard'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { ApiError, type DashboardInfo } from '@/lib/api'
import { lastDashboard } from '@/lib/last-dashboard'
import { dashboardsQuery } from '@/lib/queries'

/** The last dashboard opened on this device, else the first system one, never an archived one or one out of the sidebar (D34). */
export function pickDashboard(dashboards: DashboardInfo[], last?: number): DashboardInfo | undefined {
  const live = dashboards.filter((d) => !d.archived_at && d.sidebar)
  return live.find((d) => d.dashboard_id === last) ?? live.find((d) => d.owner === 'system') ?? live[0]
}

/**
 * "/dashboards" itself shows nothing: it picks a dashboard and redirects to it. With
 * none in the sidebar it says why: an empty install has nothing yet; a
 * hidden built-in comes back from Gallery › Dashboards, a hidden dashboard
 * of your own from its page, and an archived one from the archive, the
 * only way back once the Undo toast is gone (D17a).
 */
export default function Home() {
  const navigate = useNavigate()
  const { data, error, refetch } = useQuery(dashboardsQuery)
  const target = data ? pickDashboard(data.dashboards, lastDashboard()) : undefined

  useEffect(() => {
    if (target) navigate(`/dashboards/${target.dashboard_id}`, { replace: true })
  }, [target, navigate])

  if (error) {
    // A 401 is already on its way to the login page.
    if (error instanceof ApiError && error.status === 401) return null
    return (
      <StatusCard title="Couldn't load the dashboards" description={error.message}>
        <Button variant="outline" onClick={() => refetch()}>
          Retry
        </Button>
      </StatusCard>
    )
  }
  if (data && !target) {
    const hidden = data.dashboards.filter((d) => !d.archived_at && !d.sidebar)
    const archived = data.dashboards.some((d) => d.archived_at)
    if (hidden.length > 0) {
      const builtIn = hidden.some((d) => d.owner === 'system')
      const from = builtIn ? 'from Gallery › Dashboards' : 'from its page'
      return (
        <StatusCard
          title="Everything is hidden from the sidebar"
          description={`Show a dashboard in the sidebar again ${from}${archived ? ', or restore one from the archive' : ''}.`}
        >
          <div className="flex flex-col gap-2">
            <Button asChild variant="outline" className="w-full">
              {builtIn ? (
                <Link to="/gallery/dashboards">Open Gallery › Dashboards</Link>
              ) : (
                <Link to={`/dashboards/${hidden[0].dashboard_id}`}>Open {hidden[0].title}</Link>
              )}
            </Button>
            {archived && (
              <Button asChild variant="outline" className="w-full">
                <Link to="/archive">Open the archive</Link>
              </Button>
            )}
          </div>
        </StatusCard>
      )
    }
    if (archived) {
      return (
        <StatusCard title="Everything is archived" description="Restore a dashboard from the archive to put it back in the sidebar.">
          <Button asChild variant="outline" className="w-full">
            <Link to="/archive">Open the archive</Link>
          </Button>
        </StatusCard>
      )
    }
    return <StatusCard title="No dashboards yet" description="Ask your agent to make one: it will show up here." />
  }
  return (
    <div className="flex min-h-svh items-center justify-center">
      <Spinner className="size-6 text-muted-foreground" />
    </div>
  )
}

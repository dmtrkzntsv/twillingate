import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router'
import StatusCard from '@/components/StatusCard'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { ApiError, type DashboardInfo } from '@/lib/api'
import { lastDashboard } from '@/lib/last-dashboard'
import { dashboardsQuery } from '@/lib/queries'

/** The last dashboard opened on this device, else the first system one, never an archived one (D34). */
export function pickDashboard(dashboards: DashboardInfo[], last?: number): DashboardInfo | undefined {
  const live = dashboards.filter((d) => !d.archived_at)
  return live.find((d) => d.dashboard_id === last) ?? live.find((d) => d.owner === 'system') ?? live[0]
}

/**
 * "/" itself shows nothing: it picks a dashboard and redirects to it. With
 * none live it says why: an empty install has nothing yet, but one where
 * everything is archived links to the dashboard gallery, the only way back
 * once the Undo toast is gone (D17).
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
    if (data.dashboards.length > 0) {
      return (
        <StatusCard title="Everything is archived" description="Restore a dashboard from the gallery to put it back in the sidebar.">
          <Button asChild variant="outline" className="w-full">
            <Link to="/gallery/dashboards">Open the dashboard gallery</Link>
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

import { useQuery } from '@tanstack/react-query'
import { Navigate } from 'react-router'
import { Spinner } from '@/components/ui/spinner'
import { dashboardsQuery } from '@/lib/queries'

/**
 * "/" opens the projects. A dashboards preview (`reporting dev`) has no
 * projects to show, so it opens the dashboards instead. The dashboard list
 * says which this is; a failed read goes to the projects, which say why.
 */
export default function Landing() {
  const { data, isError } = useQuery(dashboardsQuery)
  if (data) return <Navigate to={data.dev ? '/dashboards' : '/projects'} replace />
  if (isError) return <Navigate to="/projects" replace />
  return (
    <div className="flex min-h-svh items-center justify-center">
      <Spinner className="size-6 text-muted-foreground" />
    </div>
  )
}

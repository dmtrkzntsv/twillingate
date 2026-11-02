import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import NotFound from '@/components/NotFound'
import { Button } from '@/components/ui/button'
import { dashboardsQuery } from '@/lib/queries'

/** Any /app/ address no route answers, in the app shell so the sidebar still leads somewhere. */
export default function NoSuchPage() {
  const { data } = useQuery(dashboardsQuery)
  return (
    <AppShell dashboards={data?.dashboards ?? []} currentId={0}>
      <TopBar />
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col p-3 sm:p-4 lg:p-6">
        <NotFound
          title="No page at this address"
          actions={
            <Button asChild variant="outline">
              <Link to="/projects">Go to your projects</Link>
            </Button>
          }
        >
          Check the address for a typo, or pick a dashboard from the sidebar.
        </NotFound>
      </div>
    </AppShell>
  )
}

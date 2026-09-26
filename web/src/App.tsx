import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { BrowserRouter, Route, Routes, useNavigate } from 'react-router'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { endpoints } from '@/lib/api'
import Callback from '@/pages/Callback'
import Login from '@/pages/Login'

const LAST_DASHBOARD_KEY = 'twillingate.last_dashboard'

/**
 * "/" itself shows nothing: it picks a dashboard (the last one opened on
 * this device, else the first system dashboard) and redirects to it.
 */
function Home() {
  const navigate = useNavigate()
  const { data } = useQuery({ queryKey: ['dashboards'], queryFn: endpoints.dashboards })

  useEffect(() => {
    if (!data) return
    const lastId = Number(localStorage.getItem(LAST_DASHBOARD_KEY))
    const target =
      data.dashboards.find((d) => d.dashboard_id === lastId) ??
      data.dashboards.find((d) => d.owner === 'system') ??
      data.dashboards[0]
    if (target) navigate(`/dashboards/${target.dashboard_id}`, { replace: true })
  }, [data, navigate])

  return null
}

// Placeholder for the dashboard shell Task 19 builds; keeps the route wired
// up and the app's landing content visible in the meantime.
function DashboardPlaceholder() {
  return (
    <div className="flex min-h-svh items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>twillingate</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-muted-foreground text-sm">Dashboards are on their way.</p>
        </CardContent>
      </Card>
    </div>
  )
}

function App() {
  return (
    <BrowserRouter basename="/app">
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/dashboards/:id" element={<DashboardPlaceholder />} />
        <Route path="/callback" element={<Callback />} />
        <Route path="/login" element={<Login />} />
      </Routes>
    </BrowserRouter>
  )
}

export default App

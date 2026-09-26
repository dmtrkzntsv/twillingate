import { useEffect, type ReactNode } from 'react'
import { WifiOffIcon } from 'lucide-react'
import { BrowserRouter, Route, Routes, useNavigate } from 'react-router'
import StatusCard from '@/components/StatusCard'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useOnline } from '@/hooks/use-online'
import { currentAppPath, onUnauthorized } from '@/lib/auth'
import Callback from '@/pages/Callback'
import Dashboard from '@/pages/Dashboard'
import Home from '@/pages/Home'
import Login from '@/pages/Login'

/** Sends a request the API refused with 401 to the login page, in-app, coming back here after. */
function LoginOnUnauthorized() {
  const navigate = useNavigate()
  useEffect(() => {
    onUnauthorized(() => {
      const returnTo = currentAppPath()
      // Several requests can fail at once: only the first one navigates.
      if (returnTo.startsWith('/login') || returnTo.startsWith('/callback')) return
      navigate(`/login?returnTo=${encodeURIComponent(returnTo)}`, { replace: true })
    })
  }, [navigate])
  return null
}

/** Offline, the installed app opens but shows no data (D42). */
function OnlineOnly({ children }: { children: ReactNode }) {
  const online = useOnline()
  if (online) return children
  return (
    <StatusCard title="Offline" description="Offline — showing nothing until the connection is back">
      <WifiOffIcon className="mx-auto size-6 text-muted-foreground" />
    </StatusCard>
  )
}

function App() {
  return (
    <BrowserRouter basename="/app">
      <LoginOnUnauthorized />
      <TooltipProvider>
        <Routes>
          <Route
            path="/"
            element={
              <OnlineOnly>
                <Home />
              </OnlineOnly>
            }
          />
          <Route
            path="/dashboards/:id"
            element={
              <OnlineOnly>
                <Dashboard />
              </OnlineOnly>
            }
          />
          <Route path="/callback" element={<Callback />} />
          <Route path="/login" element={<Login />} />
        </Routes>
      </TooltipProvider>
    </BrowserRouter>
  )
}

export default App

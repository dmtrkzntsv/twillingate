import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import StatusCard from '@/components/StatusCard'
import { Button } from '@/components/ui/button'
import { completeLogin } from '@/lib/auth'

function Callback() {
  const navigate = useNavigate()
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    completeLogin(window.location.search)
      .then((returnTo) => {
        if (!cancelled) navigate(returnTo, { replace: true })
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : 'login failed')
      })
    return () => {
      cancelled = true
    }
  }, [navigate])

  if (!error) return <StatusCard title="Signing in…" />
  return (
    <StatusCard title="Sign in failed" description={error}>
      <Button asChild variant="outline" className="w-full">
        <Link to="/login">Try again</Link>
      </Button>
    </StatusCard>
  )
}

export default Callback

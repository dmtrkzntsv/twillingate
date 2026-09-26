import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
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

  return (
    <div className="flex min-h-svh items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>{error ? 'Sign in failed' : 'Signing in…'}</CardTitle>
          {error && <CardDescription>{error}</CardDescription>}
        </CardHeader>
        {error && (
          <CardContent>
            <a className="text-sm underline" href="/app/login">
              Try again
            </a>
          </CardContent>
        )}
      </Card>
    </div>
  )
}

export default Callback

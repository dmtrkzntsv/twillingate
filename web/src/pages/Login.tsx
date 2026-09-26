import { useEffect, useState, type FormEvent } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import StatusCard from '@/components/StatusCard'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { authProviderHint, beginLogin, detectAuth, sanitizeReturnTo, setPastedToken } from '@/lib/auth'

type Mode = 'checking' | 'redirecting' | 'paste' | 'error'

function Login() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const returnTo = sanitizeReturnTo(params.get('returnTo'))
  const [mode, setMode] = useState<Mode>('checking')
  const [token, setToken] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [providerHint, setProviderHint] = useState<string | undefined>(undefined)

  useEffect(() => {
    let cancelled = false
    detectAuth()
      .then((outcome) => {
        if (cancelled) return
        switch (outcome) {
          case 'open':
            navigate(returnTo, { replace: true })
            break
          case 'login':
            setMode('redirecting')
            beginLogin(returnTo).catch((e: unknown) => {
              if (cancelled) return
              setError(e instanceof Error ? e.message : 'could not start login')
              setMode('error')
            })
            break
          case 'paste':
            setProviderHint(authProviderHint())
            setMode('paste')
        }
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setError(e instanceof Error ? e.message : 'could not check how to sign in')
        setMode('error')
      })
    return () => {
      cancelled = true
    }
  }, [navigate, returnTo])

  function submit(e: FormEvent) {
    e.preventDefault()
    const trimmed = token.trim()
    if (!trimmed) return
    setPastedToken(trimmed)
    navigate(returnTo, { replace: true })
  }

  const description =
    mode === 'checking'
      ? 'Checking how to sign in…'
      : mode === 'redirecting'
        ? 'Redirecting to sign in…'
        : mode === 'paste'
          ? providerHint
            ? `${providerHint} has no self-service sign-in. Paste an API token to continue.`
            : 'Paste an API token to continue.'
          : error

  return (
    <StatusCard title="Sign in" description={description}>
      {mode === 'paste' && (
        <form className="flex flex-col gap-3" onSubmit={submit}>
          <Input
            type="password"
            placeholder="Bearer token"
            aria-label="API token"
            autoFocus
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
          <Button type="submit" disabled={!token.trim()}>
            Continue
          </Button>
        </form>
      )}
    </StatusCard>
  )
}

export default Login

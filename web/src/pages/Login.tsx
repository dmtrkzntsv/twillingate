import { useEffect, useState, type FormEvent } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { ArrowRightIcon, LoaderCircleIcon } from 'lucide-react'
import IcebergLogo, { WATERLINE } from '@/components/IcebergLogo'
import Snowfall from '@/components/Snowfall'
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

  const hint =
    mode === 'paste'
      ? providerHint
        ? `${providerHint} has no self-service sign-in. Paste an API token to continue.`
        : 'Paste an API token to continue.'
      : mode === 'error'
        ? error
        : null

  return (
    <main className="relative isolate flex min-h-svh flex-col items-center justify-center overflow-hidden bg-[radial-gradient(ellipse_at_50%_42%,#0b3a66_0%,#061a30_48%,#030b15_100%)] p-6 text-[#f8fcff]">
      <Snowfall className="absolute inset-0 z-20" />

      <div className="flex flex-col items-center gap-8">
        <div className="relative size-[min(78vw,19rem)]">
          <div className="absolute -inset-[45%] bg-[radial-gradient(closest-side,rgba(46,159,229,0.4),transparent)]" />
          <div className="surface-in relative z-10 size-full overflow-hidden rounded-[22%] shadow-[0_24px_60px_-24px_rgba(0,0,0,0.8)] ring-1 ring-white/15">
            <IcebergLogo className="size-full" />
          </div>

          <div
            className="surface-in absolute left-1/2 z-30 w-[90%] -translate-x-1/2 -translate-y-1/2 [animation-delay:150ms]"
            style={{ top: `${WATERLINE * 100}%` }}
          >
            {mode === 'paste' ? (
              <form onSubmit={submit} className={band + ' focus-within:border-[#8fd3ff]/70 focus-within:ring-4 focus-within:ring-[#8fd3ff]/25'}>
                <input
                  type="password"
                  placeholder="API token"
                  aria-label="API token"
                  autoFocus
                  autoComplete="current-password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  className="h-full min-w-0 flex-1 bg-transparent pl-5 text-sm tracking-wide text-[#f8fcff] outline-none placeholder:text-[#8fd3ff]/60"
                />
                <button
                  type="submit"
                  aria-label="Sign in"
                  disabled={!token.trim()}
                  className="mr-1.5 flex size-9 shrink-0 items-center justify-center rounded-full bg-[#f8fcff] text-[#073f7c] transition hover:bg-[#8fd3ff] focus-visible:ring-4 focus-visible:ring-[#8fd3ff]/50 focus-visible:outline-none disabled:bg-white/15 disabled:text-[#8fd3ff]/50"
                >
                  <ArrowRightIcon className="size-4" />
                </button>
              </form>
            ) : mode === 'error' ? (
              <div className={band + ' justify-between pl-5'}>
                <span className="text-sm text-[#ffd0c8]">Sign-in didn't start</span>
                <button
                  type="button"
                  onClick={() => window.location.reload()}
                  className="mr-1.5 h-9 rounded-full bg-[#f8fcff] px-4 text-sm font-medium text-[#073f7c] transition hover:bg-[#8fd3ff] focus-visible:ring-4 focus-visible:ring-[#8fd3ff]/50 focus-visible:outline-none"
                >
                  Try again
                </button>
              </div>
            ) : (
              <div className={band + ' justify-center gap-2.5 text-sm text-[#8fd3ff]'} role="status">
                <LoaderCircleIcon className="size-4 animate-spin" aria-hidden="true" />
                {mode === 'checking' ? 'Checking how to sign in…' : 'Redirecting to sign in…'}
              </div>
            )}
          </div>
        </div>

        <div className="surface-in relative z-10 flex max-w-xs flex-col items-center gap-2 text-center [animation-delay:250ms]">
          <h1 className="text-2xl font-semibold tracking-tight">twillingate</h1>
          {hint && <p className="text-sm text-balance text-[#8fd3ff]/75">{hint}</p>}
        </div>
      </div>
    </main>
  )
}

/** The glass strip laid across the logo's waterline. */
const band =
  'flex h-12 items-center rounded-full border border-white/20 bg-[#04111f]/65 shadow-[0_12px_40px_-8px_rgba(3,11,21,0.8)] backdrop-blur-md transition'

export default Login

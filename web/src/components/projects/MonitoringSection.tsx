import { useCallback, useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router'
import LoadError from '@/components/projects/LoadError'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import type { Monitor, MonitorBody, MonitorDomain } from '@/lib/api'
import { plural } from '@/lib/format'
import { monitorsQuery, projectTabsQuery } from '@/lib/queries'
import { rangeParams, tabPath } from '@/lib/project-tabs'
import { daysUntil, daysUntilDate, formatInterval } from '@/lib/time'
import { formatAgo } from '@/lib/units'

interface Props {
  projectId: number
  pending?: boolean
  /** Saves one origin's monitor; resolves true when saved. */
  onSave: (body: MonitorBody) => Promise<boolean>
  /** Asks for a manual check of one origin; a refusal is the caller's to report. */
  onCheck: (origin: string) => Promise<boolean>
}

/** Hops from which the chain is worth a warning (the check still passes up to 5). */
const LONG_CHAIN = 3

/** How often a pending manual check is looked at, when the page stops waiting for it (the runner serves one within seconds, or may be down), and how often after that. */
const CHECK_POLL_MS = 2_000
const CHECK_GIVE_UP_MS = 45_000
const CHECK_SLOW_POLL_MS = 15_000

/** The manual check still to finish on an enabled monitor, by when it was asked for. */
function pendingCheck(m: Monitor): string | null {
  const { requested_at: requested, finished_at: finished } = m.check
  return m.enabled && requested !== '' && (finished === '' || finished < requested) ? requested : null
}

/** How the last manual check went. */
function checkText(check: Monitor['check'], now: Date): string {
  const when = formatAgo(check.finished_at, now)
  if (check.up) return `up, ${check.status}, ${check.ms} ms (${when})`
  if (!check.reason) return `${check.error} (${when})`
  return `down, ${check.reason}: ${check.error} (${when})`
}

/** "73 days", or "expired" once the day has passed. */
function left(days: number): string {
  return days < 0 ? 'expired' : plural(days, 'day')
}

/** What Settings says in full about an origin's certificate. */
function certificateText(origin: string, cert: Monitor['cert'], now: Date): string {
  if (origin.startsWith('http://')) return 'none on a plain http:// origin'
  if (!cert.expires_at) return cert.error || 'not checked yet'
  const expiry = `expires ${cert.expires_at.slice(0, 10)} (${left(daysUntil(new Date(cert.expires_at), now))})`
  if (!cert.valid) return cert.error ? `invalid, ${expiry}: ${cert.error}` : `invalid, ${expiry}`
  if (cert.error) return `${expiry}; last check failed: ${cert.error}`
  return expiry
}

/** The certificate in a word or two on the row; null when it has none to speak of. */
function certificateShort(m: Monitor, now: Date): string | null {
  if (m.origin.startsWith('http://')) return null
  if (m.cert.expires_at) return `cert ${left(daysUntil(new Date(m.cert.expires_at), now))}`
  return m.cert.error ? 'cert unknown' : 'cert not checked yet'
}

function domainText(d: MonitorDomain, now: Date): string {
  if (!d.expires_at) return `Domain: ${d.domain} unknown`
  return `Domain: ${d.domain} renews ${d.expires_at} (${left(daysUntilDate(d.expires_at, now))})`
}

interface RowProps {
  m: Monitor
  now: Date
  pending?: boolean
  /** The check the page stopped waiting for on this row, by when it was asked for. */
  gaveUp: string | undefined
  onGiveUp: (origin: string, requestedAt: string) => void
  onSave: Props['onSave']
  onCheck: () => void
}

/**
 * One origin: a switch that saves at once and, while on, what the checks found,
 * Check now, and an optional text with its own Save. An off row is just the two.
 */
function MonitorRow({ m, now, pending, gaveUp, onGiveUp, onSave, onCheck }: RowProps) {
  // Where the switch goes while its save is out; a failed one drops back.
  const [switching, setSwitching] = useState<boolean | null>(null)
  const [contains, setContains] = useState(m.contains)
  const containsId = useId()
  // An empty text field asked for stays open until saved.
  const [askedForText, setAskedForText] = useState(false)
  // A text not saved yet: a refetch (polling, window focus, another row's save)
  // must not throw it away.
  const edited = useRef(false)

  // Start from what is saved, again after each save.
  useEffect(() => {
    if (!edited.current) setContains(m.contains)
  }, [m.contains])

  const waiting = pendingCheck(m)
  useEffect(() => {
    if (!waiting) return
    const timer = setTimeout(() => onGiveUp(m.origin, waiting), CHECK_GIVE_UP_MS)
    return () => clearTimeout(timer)
  }, [waiting, m.origin, onGiveUp])

  const toggle = (on: boolean) => {
    setSwitching(on)
    void onSave({ origin: m.origin, enabled: on }).finally(() => setSwitching(null))
  }
  const textEdited = contains.trim() !== m.contains
  const save = (e: FormEvent) => {
    e.preventDefault()
    if (!textEdited) return // Enter in the text field, with nothing to save
    const text = contains.trim()
    void onSave({ origin: m.origin, contains: text }).then((saved) => {
      if (saved) {
        edited.current = false
        setAskedForText(false)
        setContains(text)
      }
    })
  }

  const checking = waiting !== null && waiting !== gaveUp
  const certFull = certificateText(m.origin, m.cert, now)
  const certProblem = !m.origin.startsWith('http://') && (m.cert.error !== '' || (m.cert.expires_at !== '' && !m.cert.valid))
  const certShort = certificateShort(m, now)

  return (
    <li aria-label={m.origin} className="flex items-start gap-3 py-3 first:pt-0 last:pb-0">
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <span className="min-w-0 text-sm font-medium break-all">{m.origin}</span>
          {m.enabled && (
            <>
              <span className="min-w-0 text-sm break-words text-muted-foreground">
                {m.check.finished_at && <span>{checkText(m.check, now)}</span>}
                {m.check.finished_at && certShort && ' · '}
                {certShort && <span title={certFull}>{certShort}</span>}
              </span>
              <Button type="button" size="sm" variant="outline" className="ml-auto" disabled={pending || checking} onClick={onCheck}>
                {checking ? 'Checking…' : 'Check now'}
              </Button>
            </>
          )}
        </div>
        {m.enabled && (
          <>
            {certProblem && <p className="min-w-0 text-sm break-words text-muted-foreground">{`Certificate: ${certFull}`}</p>}
            {m.redirects >= LONG_CHAIN && <p className="text-sm text-muted-foreground">{`Redirects ${m.redirects} times before answering, which slows every visit`}</p>}
            {waiting && waiting === gaveUp && <p role="status" className="text-sm text-muted-foreground">The check has not finished yet: its result will show here when the server has run it</p>}
            {contains !== '' || m.contains !== '' || askedForText ? (
              <form onSubmit={save} className="flex min-w-0 flex-col gap-1.5">
                <Label htmlFor={containsId}>Page must contain</Label>
                <div className="flex min-w-0 items-center gap-2">
                  <Input
                    id={containsId}
                    className="min-w-0"
                    value={contains}
                    disabled={pending}
                    maxLength={200}
                    // Mounted by the link's press, the only time this is set and the field new.
                    autoFocus={askedForText}
                    onChange={(e) => {
                      edited.current = true
                      setContains(e.target.value)
                    }}
                  />
                  {textEdited && <Button type="submit" size="sm" disabled={pending}>Save</Button>}
                </div>
              </form>
            ) : (
              <Button
                type="button"
                variant="link"
                className="h-auto w-fit p-0 text-sm font-normal text-muted-foreground underline"
                disabled={pending}
                onClick={() => setAskedForText(true)}
              >
                Also look for text on the page
              </Button>
            )}
          </>
        )}
      </div>
      <Switch aria-label={`Monitor ${m.origin}`} className="mt-0.5 shrink-0" checked={switching ?? m.enabled} disabled={pending} onCheckedChange={toggle} />
    </li>
  )
}

/**
 * A project's availability monitors: one row per eligible origin, each with its
 * own switch; below them the domains they share and the Availability tab.
 */
export default function MonitoringSection({ projectId, pending, onSave, onCheck }: Props) {
  // The checks the page stopped waiting for, by origin: it looks less often,
  // and offers the row's button again, whose press the server answers.
  const [gaveUp, setGaveUp] = useState<Record<string, string>>({})
  const q = useQuery({
    ...monitorsQuery(projectId),
    refetchInterval: (query) => {
      const waiting = (query.state.data?.monitors ?? []).filter((m) => pendingCheck(m) !== null)
      if (waiting.length === 0) return false
      return waiting.every((m) => pendingCheck(m) === gaveUp[m.origin]) ? CHECK_SLOW_POLL_MS : CHECK_POLL_MS
    },
  })
  const tabs = useQuery(projectTabsQuery(projectId)).data?.tabs
  const [url] = useSearchParams()
  const data = q.data
  const giveUp = useCallback((origin: string, requestedAt: string) => setGaveUp((g) => ({ ...g, [origin]: requestedAt })), [])

  if (q.isError) {
    return (
      <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
        <h2 className="text-base font-semibold">Monitoring</h2>
        <LoadError what="the monitors" error={q.error} onRetry={() => void q.refetch()} />
      </section>
    )
  }
  if (!data) return null

  if (data.monitors.length === 0) {
    return (
      <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
        <h2 className="text-base font-semibold">Monitoring</h2>
        <p className="text-sm text-muted-foreground">Add an exact http(s) origin above to monitor it.</p>
      </section>
    )
  }
  const anyOn = data.monitors.some((m) => m.enabled)
  const availability = tabs?.find((t) => t.owner === 'system' && t.title === 'Availability')
  // A refusal may mean a check this page has not heard of, from another tab
  // or the CLI: read the monitors, so it shows.
  const check = (origin: string) => {
    void onCheck(origin).then((asked) => {
      if (!asked) void q.refetch()
    })
  }
  const now = new Date()

  return (
    <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <h2 className="text-base font-semibold">Monitoring</h2>
        {anyOn && <p className="text-sm text-muted-foreground">{`each site is checked every ${formatInterval(data.check_every_seconds)}`}</p>}
      </div>
      {!anyOn && <p className="text-sm text-muted-foreground">Check that the site is up, and when its certificate and domain expire.</p>}
      <ul className="flex flex-col divide-y">
        {data.monitors.map((m) => (
          <MonitorRow
            key={`${projectId}:${m.origin}`}
            m={m}
            now={now}
            pending={pending}
            gaveUp={gaveUp[m.origin]}
            onGiveUp={giveUp}
            onSave={onSave}
            onCheck={() => check(m.origin)}
          />
        ))}
      </ul>
      {data.domains.map((d) => (
        <p key={d.domain} className="min-w-0 text-sm break-words text-muted-foreground">{domainText(d, now)}</p>
      ))}
      {anyOn && availability && (
        <div>
          <Link className="text-sm underline underline-offset-4" to={tabPath(projectId, availability.dashboard_id, rangeParams(url).toString())}>
            Open Availability
          </Link>
        </div>
      )}
    </section>
  )
}

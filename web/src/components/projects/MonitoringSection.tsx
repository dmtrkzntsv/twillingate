import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router'
import LoadError from '@/components/projects/LoadError'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import type { Monitor, MonitorBody } from '@/lib/api'
import { plural } from '@/lib/format'
import { monitorQuery, projectTabsQuery } from '@/lib/queries'
import { rangeParams, tabPath } from '@/lib/project-tabs'
import { daysUntil, daysUntilDate, formatInterval } from '@/lib/time'
import { formatAgo } from '@/lib/units'

interface Props {
  projectId: number
  pending?: boolean
  /** Saves the monitor; resolves true when saved. */
  onSave: (body: MonitorBody) => Promise<boolean>
  /** Asks for a manual check; a refusal is the caller's to report. */
  onCheck: () => Promise<boolean>
}

/** Hops from which the chain is worth a warning (the check still passes up to 5). */
const LONG_CHAIN = 3

/** How often a pending manual check is looked at, and when the page stops waiting (the runner serves one within seconds, or never). */
const CHECK_POLL_MS = 2_000
const CHECK_GIVE_UP_MS = 45_000

/** The manual check still to finish on an enabled monitor, by when it was asked for. */
function pendingCheck(m: Monitor | undefined): string | null {
  return m?.enabled && m.check.requested_at > m.check.finished_at ? m.check.requested_at : null
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

/** What Settings says about the monitored origin's certificate. */
function certificateText(origin: string, cert: Monitor['cert'], now: Date): string {
  if (origin.startsWith('http://')) return 'none on a plain http:// origin'
  if (!cert.expires_at) return cert.error || 'not checked yet'
  const expiry = `expires ${cert.expires_at.slice(0, 10)} (${left(daysUntil(new Date(cert.expires_at), now))})`
  if (!cert.valid) return cert.error ? `invalid, ${expiry}: ${cert.error}` : `invalid, ${expiry}`
  if (cert.error) return `${expiry}; last check failed: ${cert.error}`
  return expiry
}

/**
 * A project's availability monitor: a switch, and once on the origin to check
 * and an optional text the page must hold. Save appears only with an edit to
 * save; at rest, what the checks found, Check now and the Availability tab.
 */
export default function MonitoringSection({ projectId, pending, onSave, onCheck }: Props) {
  // The check the page stopped waiting for: it polls no more, and offers the
  // button again, whose press the server answers.
  const [gaveUp, setGaveUp] = useState<string | null>(null)
  const q = useQuery({
    ...monitorQuery(projectId),
    refetchInterval: (query) => {
      const waiting = pendingCheck(query.state.data)
      return waiting && waiting !== gaveUp ? CHECK_POLL_MS : false
    },
  })
  const tabs = useQuery(projectTabsQuery(projectId)).data?.tabs
  const [url] = useSearchParams()
  const m = q.data
  const [enabled, setEnabled] = useState(false)
  const [origin, setOrigin] = useState('')
  const [contains, setContains] = useState('')
  const containsId = useId()
  // The project whose empty text field was asked for: it stays open until saved.
  const [askedForText, setAskedForText] = useState<number | null>(null)
  // The project whose form holds an edit not saved yet: a refetch (window
  // focus, another section's save) must not throw it away.
  const edited = useRef<number | null>(null)
  const edit = () => {
    edited.current = projectId
  }

  // Start from what is saved, again after each save or a project change.
  useEffect(() => {
    if (!m || edited.current === m.project_id) return
    edited.current = null
    setEnabled(m.enabled)
    setOrigin(m.origin || m.eligible_origins[0] || '')
    setContains(m.contains)
  }, [m])

  const waiting = pendingCheck(m)
  useEffect(() => {
    if (!waiting) return
    const timer = setTimeout(() => setGaveUp(waiting), CHECK_GIVE_UP_MS)
    return () => clearTimeout(timer)
  }, [waiting])

  if (q.isError) {
    return (
      <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
        <h2 className="text-base font-semibold">Monitoring</h2>
        <LoadError what="the monitor" error={q.error} onRetry={() => void q.refetch()} />
      </section>
    )
  }
  if (!m) return null

  if (m.eligible_origins.length === 0) {
    return (
      <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
        <h2 className="text-base font-semibold">Monitoring</h2>
        <p className="text-sm text-muted-foreground">Add an exact http(s) origin above to monitor it.</p>
      </section>
    )
  }
  const chosen = m.eligible_origins.includes(origin) ? origin : m.eligible_origins[0]
  const availability = tabs?.find((t) => t.owner === 'system' && t.title === 'Availability')
  // Against what is saved, so an edit undone asks for nothing. The fields are
  // hidden with the switch off, so what they hold counts only with it on.
  const edits = enabled !== m.enabled || (enabled && (chosen !== (m.origin || m.eligible_origins[0]) || contains.trim() !== m.contains))
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!edits) return // Enter in the text field, with nothing to save
    void onSave({ enabled, origin: chosen, contains: contains.trim() }).then((saved) => {
      if (saved) {
        edited.current = null
        setAskedForText(null)
      }
    })
  }
  const now = new Date()
  const checking = waiting !== null && waiting !== gaveUp
  const interval = formatInterval(m.interval_seconds)
  const domainText = m.domain.expires_at ? `renews ${m.domain.expires_at} (${left(daysUntilDate(m.domain.expires_at, now))})` : 'unknown'

  return (
    <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-base font-semibold">Monitoring</h2>
        <Switch
          aria-label="Monitor this site"
          checked={enabled}
          disabled={pending}
          onCheckedChange={(on) => {
            edit()
            setEnabled(on)
          }}
        />
      </div>
      {!enabled && <p className="text-sm text-muted-foreground">Check that the site is up, and when its certificate and domain expire.</p>}
      {(enabled || edits) && (
        <form onSubmit={submit} className="grid grid-cols-1 gap-3">
          {enabled &&
            (m.eligible_origins.length === 1 ? (
              <p className="min-w-0 text-sm break-all text-muted-foreground">{`${m.eligible_origins[0]} · every ${interval}`}</p>
            ) : (
              <div className="flex min-w-0 flex-col gap-1.5">
                <Select
                  value={chosen}
                  disabled={pending}
                  onValueChange={(o) => {
                    // Inside a form Radix also fires "" when the value is set from
                    // here, as the saved origin is: that is no choice of the user's.
                    if (!o) return
                    edit()
                    setOrigin(o)
                  }}
                >
                  <SelectTrigger aria-label="Origin to monitor" className="w-full max-w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent position="popper" align="start">
                    {m.eligible_origins.map((o) => (
                      <SelectItem key={o} value={o} className="break-all">{o}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-sm text-muted-foreground">{`Checked every ${interval}`}</p>
              </div>
            ))}
          {enabled &&
            (contains !== '' || m.contains !== '' || askedForText === projectId ? (
              <div className="flex min-w-0 flex-col gap-1.5">
                <Label htmlFor={containsId}>Page must contain</Label>
                <Input
                  id={containsId}
                  value={contains}
                  disabled={pending}
                  maxLength={200}
                  // Mounted by the link's press, the only time this is set and the field new.
                  autoFocus={askedForText === projectId}
                  onChange={(e) => {
                    edit()
                    setContains(e.target.value)
                  }}
                />
              </div>
            ) : (
              <Button
                type="button"
                variant="link"
                className="h-auto w-fit p-0 text-sm font-normal text-muted-foreground underline"
                disabled={pending}
                onClick={() => setAskedForText(projectId)}
              >
                Also look for text on the page
              </Button>
            ))}
          {edits && (
            <div>
              <Button type="submit" size="sm" disabled={pending}>Save</Button>
            </div>
          )}
        </form>
      )}
      {m.enabled && !edits && (
        <>
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
            <dt className="text-muted-foreground">Certificate</dt>
            <dd className="min-w-0 break-words">{certificateText(m.origin, m.cert, now)}</dd>
            <dt className="text-muted-foreground">Domain</dt>
            <dd className="min-w-0 break-words">{domainText}</dd>
            {m.check.finished_at && (
              <>
                <dt className="text-muted-foreground">Last check</dt>
                <dd className="min-w-0 break-words">{checkText(m.check, now)}</dd>
              </>
            )}
          </dl>
          {m.redirects >= LONG_CHAIN && <p className="text-sm text-muted-foreground">{`Redirects ${m.redirects} times before answering, which slows every visit`}</p>}
          {waiting && waiting === gaveUp && <p role="status" className="text-sm text-muted-foreground">The check has not finished yet: its result will show here when the server has run it</p>}
          <div className="flex flex-wrap items-center gap-3">
            <Button type="button" size="sm" variant="outline" disabled={pending || checking} onClick={() => void onCheck()}>
              {checking ? 'Checking…' : 'Check now'}
            </Button>
            {availability && (
              <Link className="text-sm underline underline-offset-4" to={tabPath(projectId, availability.dashboard_id, rangeParams(url).toString())}>
                Open Availability
              </Link>
            )}
          </div>
        </>
      )}
    </section>
  )
}

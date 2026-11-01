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

interface Props {
  projectId: number
  pending?: boolean
  /** Saves the monitor; resolves true when saved. */
  onSave: (body: MonitorBody) => Promise<boolean>
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
 * A project's availability monitor: on or off, the origin to check, and an
 * optional text the page must hold. Below, once on, what the checks found
 * (certificate, domain renewal) and a link to the Availability tab.
 */
export default function MonitoringSection({ projectId, pending, onSave }: Props) {
  const q = useQuery(monitorQuery(projectId))
  const tabs = useQuery(projectTabsQuery(projectId)).data?.tabs
  const [url] = useSearchParams()
  const m = q.data
  const [enabled, setEnabled] = useState(false)
  const [origin, setOrigin] = useState('')
  const [contains, setContains] = useState('')
  const containsId = useId()
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

  if (q.isError) {
    return (
      <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
        <h2 className="text-base font-semibold">Monitoring</h2>
        <LoadError what="the monitor" error={q.error} onRetry={() => void q.refetch()} />
      </section>
    )
  }
  if (!m) return null

  const chosen = m.eligible_origins.includes(origin) ? origin : (m.eligible_origins[0] ?? '')
  const availability = tabs?.find((t) => t.owner === 'system' && t.title === 'Availability')
  if (m.eligible_origins.length === 0) {
    return (
      <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
        <h2 className="text-base font-semibold">Monitoring</h2>
        <p className="text-sm text-muted-foreground">Add an http(s) origin above to monitor it</p>
      </section>
    )
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void onSave({ enabled, origin: chosen, contains: contains.trim() }).then((saved) => {
      if (saved) edited.current = null
    })
  }
  const now = new Date()
  const domainText = m.domain.expires_at ? `renews ${m.domain.expires_at} (${left(daysUntilDate(m.domain.expires_at, now))})` : 'unknown'

  return (
    <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
      <h2 className="text-base font-semibold">Monitoring</h2>
      <form onSubmit={submit} className="grid grid-cols-1 gap-3">
        <div className="flex items-center gap-2">
          <Switch
            id={`${containsId}-on`}
            checked={enabled}
            disabled={pending}
            onCheckedChange={(on) => {
              edit()
              setEnabled(on)
            }}
          />
          <Label htmlFor={`${containsId}-on`}>Check this project's availability</Label>
        </div>
        {m.eligible_origins.length === 1 ? (
          <p className="text-sm">
            Origin to monitor: <code className="break-all">{m.eligible_origins[0]}</code>
          </p>
        ) : (
          <Select
            value={chosen}
            disabled={pending}
            onValueChange={(o) => {
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
        )}
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={containsId}>Page must contain (optional)</Label>
          <Input
            id={containsId}
            value={contains}
            disabled={pending}
            maxLength={200}
            onChange={(e) => {
              edit()
              setContains(e.target.value)
            }}
          />
        </div>
        <div>
          <Button type="submit" size="sm" disabled={pending}>Save</Button>
        </div>
      </form>
      {m.enabled && (
        <div className="flex flex-col gap-1 text-sm text-muted-foreground">
          <p>{`Checked every ${formatInterval(m.interval_seconds)}`}</p>
          <p className="break-words">{`Certificate: ${certificateText(m.origin, m.cert, now)}`}</p>
          <p>{`Domain: ${domainText}`}</p>
          {availability && (
            <Link className="w-fit text-foreground underline underline-offset-4" to={tabPath(projectId, availability.dashboard_id, rangeParams(url).toString())}>
              Open the Availability tab
            </Link>
          )}
        </div>
      )}
    </section>
  )
}

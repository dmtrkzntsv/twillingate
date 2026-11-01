import { useEffect, useId, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import type { MonitorBody } from '@/lib/api'
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

  // Start from what is saved, again after each save or a project change.
  useEffect(() => {
    if (!m) return
    setEnabled(m.enabled)
    setOrigin(m.origin || m.eligible_origins[0] || '')
    setContains(m.contains)
  }, [m])

  if (q.isError) {
    return (
      <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
        <h2 className="text-base font-semibold">Monitoring</h2>
        <p className="text-sm text-muted-foreground">
          Could not load the monitor. <Button variant="link" className="h-auto p-0" onClick={() => void q.refetch()}>Retry</Button>
        </p>
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
    void onSave({ enabled, origin: chosen, contains: contains.trim() })
  }
  const now = new Date()
  const cert = m.cert
  const certText = cert.error
    ? cert.error
    : cert.expires_at
      ? `expires ${cert.expires_at.slice(0, 10)} (${left(daysUntil(new Date(cert.expires_at), now))})`
      : 'not checked yet'
  const domainText = m.domain.expires_at ? `renews ${m.domain.expires_at} (${left(daysUntilDate(m.domain.expires_at, now))})` : 'unknown'

  return (
    <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
      <h2 className="text-base font-semibold">Monitoring</h2>
      <form onSubmit={submit} className="grid grid-cols-1 gap-3">
        <div className="flex items-center gap-2">
          <Switch id={`${containsId}-on`} aria-label="Monitor this project" checked={enabled} disabled={pending} onCheckedChange={setEnabled} />
          <Label htmlFor={`${containsId}-on`}>Check this project's availability</Label>
        </div>
        {m.eligible_origins.length === 1 ? (
          <code className="text-sm break-all">{m.eligible_origins[0]}</code>
        ) : (
          <Select value={chosen} onValueChange={setOrigin}>
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
          <Input id={containsId} value={contains} onChange={(e) => setContains(e.target.value)} />
        </div>
        <div>
          <Button type="submit" size="sm" disabled={pending}>Save</Button>
        </div>
      </form>
      {m.enabled && (
        <div className="flex flex-col gap-1 text-sm text-muted-foreground">
          <p>{`Checked every ${formatInterval(m.interval_seconds)}`}</p>
          <p className="break-words">{`Certificate: ${certText}`}</p>
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

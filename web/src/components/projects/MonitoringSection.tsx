import { useCallback, useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { MoreHorizontalIcon, RefreshCwIcon } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import LoadError from '@/components/projects/LoadError'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { Monitor, MonitorBody } from '@/lib/api'
import { formatExact } from '@/lib/format'
import { monitorsQuery, projectTabsQuery } from '@/lib/queries'
import { rangeParams, tabPath } from '@/lib/project-tabs'
import { daysUntil, formatDaysLeft, formatInterval } from '@/lib/time'
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
/** How often a page left open looks for the scheduled checks' results. */
const STATUS_POLL_MS = 60_000

/** The manual check still to finish on an enabled monitor, by when it was asked for. */
function pendingCheck(m: Monitor): string | null {
  const { requested_at: requested, finished_at: finished } = m.check
  return m.enabled && requested !== '' && (finished === '' || finished < requested) ? requested : null
}

/** Whether the last manual check ended with no result of its own: the server was down or stopping. */
function checkNotRun(m: Monitor): boolean {
  const { requested_at: requested, finished_at: finished } = m.check
  return requested !== '' && finished >= requested && m.last.checked_at < requested
}

/** A check's exact time and, when it got one, its HTTP status. */
function checkedTitle(last: Monitor['last']): string | undefined {
  if (!last.checked_at) return undefined
  const at = `${last.checked_at.slice(0, 16).replace('T', ' ')} UTC`
  return last.status > 0 ? `HTTP ${last.status}, ${at}` : at
}

/** The dot and the words under an origin: the state of a row that is on. */
function StatusLine({ m, now, checking, paused }: { m: Monitor; now: Date; checking: boolean; paused: boolean }) {
  const { last } = m
  const checked = last.checked_at !== ''
  let dot = 'bg-muted-foreground/40'
  let words = <>Not checked yet</>
  if (paused) words = <>Paused</>
  else if (checking) words = <>Checking…</>
  else if (checked && last.up) {
    dot = 'bg-emerald-500'
    words = <>{`Up, ${last.status}, ${formatExact(last.ms)} ms, ${formatAgo(last.checked_at, now)}`}</>
  } else if (checked) {
    dot = 'bg-destructive'
    words = <><span className="text-destructive">Down</span>{`, ${last.error || last.reason}, ${formatAgo(last.checked_at, now)}`}</>
  }
  return (
    <p className="flex min-w-0 items-start gap-1.5 text-sm text-muted-foreground" title={checkedTitle(last)}>
      <span aria-hidden className={`mt-1.5 size-2 shrink-0 rounded-full ${dot}`} />
      <span className="min-w-0 break-words">{words}</span>
    </p>
  )
}

/** What Settings says in full about an origin's certificate. */
function certificateText(origin: string, cert: Monitor['cert'], now: Date): string {
  if (origin.startsWith('http://')) return 'none on a plain http:// origin'
  if (!cert.expires_at) return cert.error || 'not checked yet'
  const expiry = `expires ${cert.expires_at.slice(0, 10)} (${formatDaysLeft(daysUntil(new Date(cert.expires_at), now))})`
  if (!cert.valid) return cert.error ? `invalid, ${expiry}: ${cert.error}` : `invalid, ${expiry}`
  if (cert.error) return `${expiry}; last check failed: ${cert.error}`
  return expiry
}

/** The certificate in a word or two on the row, and whether that is a problem; null on a plain http:// origin, which has none. */
function certificateShort(m: Monitor, now: Date): { text: string; bad: boolean } | null {
  if (m.origin.startsWith('http://')) return null
  if (!m.cert.expires_at) return { text: 'Cert unknown', bad: false }
  const days = daysUntil(new Date(m.cert.expires_at), now)
  if (days < 0) return { text: `Cert ${formatDaysLeft(days)}`, bad: true }
  if (!m.cert.valid) return { text: 'Cert invalid', bad: true }
  return { text: `Cert ${formatDaysLeft(days)}`, bad: false }
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
  /** Monitoring is off on this install, so Check now is too. */
  paused: boolean
}

/**
 * One origin: a switch that saves at once, then a stacked block of the origin,
 * its page text, the status line and any notes, and to the right the certificate
 * and two buttons (Check now; a menu with the page text). An off row is the switch
 * and the origin alone. On a phone the certificate is a line of the block: the
 * grid places the one element either way.
 */
function MonitorRow({ m, now, pending, gaveUp, onGiveUp, onSave, onCheck, paused }: RowProps) {
  // Where the switch goes while its save is out; a failed one drops back.
  const [switching, setSwitching] = useState<boolean | null>(null)
  const [contains, setContains] = useState(m.contains)
  const containsId = useId()
  // The text field, asked for from the menu or the saved text, stays open until saved.
  const [editing, setEditing] = useState(false)
  // The text the server had when the field last followed it: a refetch (polling,
  // window focus, another row's save) moves the field only while it still shows that.
  const followed = useRef(m.contains)
  // Closing the field hands focus back, once it is gone.
  const closing = useRef(false)
  const menuRef = useRef<HTMLButtonElement>(null)
  const savedRef = useRef<HTMLButtonElement>(null)
  // The menu's item opened the field, so closing the menu focuses it.
  const opening = useRef(false)

  // Start from what is saved, again after each save; a real edit stays.
  useEffect(() => {
    const was = followed.current
    followed.current = m.contains
    setContains((c) => (c === was ? m.contains : c))
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
        // What was saved is what the field follows from here on.
        followed.current = text
        setEditing(false)
        setContains(text)
      }
    })
  }

  const checking = waiting !== null && waiting !== gaveUp
  const certFull = certificateText(m.origin, m.cert, now)
  // A site that is down fails its certificate read the same way: the status line says it already.
  const sameAsDown = !paused && !checking && m.last.checked_at !== '' && !m.last.up && m.cert.error === m.last.error
  const certProblem = !m.origin.startsWith('http://') && !sameAsDown && (m.cert.error !== '' || (m.cert.expires_at !== '' && !m.cert.valid))
  const cert = certificateShort(m, now)
  const inputRef = useRef<HTMLInputElement>(null)
  const showField = editing || contains !== m.contains
  const cancel = () => {
    closing.current = true
    setContains(m.contains)
    setEditing(false)
  }
  useEffect(() => {
    if (!closing.current || showField) return
    closing.current = false
    ;(savedRef.current ?? menuRef.current)?.focus()
  }, [showField])
  const label = `Check ${m.origin} now`
  const notRun = !checking && !paused && checkNotRun(m)
  const hasNotes = notRun || certProblem || m.redirects >= LONG_CHAIN || (waiting !== null && waiting === gaveUp) || showField

  return (
    <li aria-label={m.origin} className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-start gap-x-3 sm:grid-cols-[auto_minmax(0,1fr)_6rem_auto]">
      <Switch aria-label={`Monitor ${m.origin}`} className="col-start-1 row-start-1 mt-px" checked={switching ?? m.enabled} disabled={pending} onCheckedChange={toggle} />
      <div className="col-start-2 row-start-1 flex min-w-0 flex-col gap-0.5">
        <span className={`min-w-0 text-sm font-medium break-all ${m.enabled ? '' : 'text-muted-foreground'}`}>{m.origin}</span>
        {m.enabled && m.contains !== '' && !showField && (
          <button
            type="button"
            ref={savedRef}
            className="w-fit max-w-full text-left text-sm break-words text-muted-foreground underline-offset-2 hover:underline"
            disabled={pending}
            onClick={() => setEditing(true)}
          >
            {`must contain "${m.contains}"`}
          </button>
        )}
        {m.enabled && <StatusLine m={m} now={now} checking={checking} paused={paused} />}
      </div>
      {m.enabled && (
        <>
          {cert && (
            <span
              className={`col-start-2 row-start-2 mt-0.5 text-sm whitespace-nowrap sm:col-start-3 sm:row-start-1 sm:mt-0 ${cert.bad ? 'text-destructive' : 'text-muted-foreground'}`}
              title={certFull}
            >
              {cert.text}
            </span>
          )}
          <div className="col-start-3 row-start-1 -mt-1.5 flex items-center gap-1 sm:col-start-4">
            <Tooltip>
              <TooltipTrigger asChild>
                <Button type="button" size="icon-sm" variant="ghost" aria-label={label} disabled={pending || checking || paused} onClick={onCheck}>
                  <RefreshCwIcon className={checking ? 'animate-spin motion-reduce:animate-none' : undefined} />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Check now</TooltipContent>
            </Tooltip>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button ref={menuRef} type="button" size="icon-sm" variant="ghost" aria-label={`More for ${m.origin}`} disabled={pending}>
                  <MoreHorizontalIcon />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" // Focus goes to the field the item opened, not back to the trigger.
              onCloseAutoFocus={(e) => {
                // Escape or a click away leaves focus to the trigger.
                if (!opening.current) return
                opening.current = false
                e.preventDefault()
                inputRef.current?.focus()
              }}>
                <DropdownMenuItem onSelect={() => {
                  opening.current = true
                  setEditing(true)
                }}>Look for text on the page</DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
          {hasNotes && <div className="col-start-2 row-start-3 mt-0.5 flex min-w-0 flex-col gap-1 sm:row-start-2">
            {notRun && <p className="text-sm text-muted-foreground">The last check did not run</p>}
            {certProblem && <p className="min-w-0 text-sm break-words text-muted-foreground">{`Certificate: ${certFull}`}</p>}
            {m.redirects >= LONG_CHAIN && <p className="text-sm text-muted-foreground">{`Redirects ${m.redirects} times before answering, which slows every visit`}</p>}
            {waiting && waiting === gaveUp && <p role="status" className="text-sm text-muted-foreground">The check has not finished yet: its result will show here when the server has run it</p>}
            {showField && (
              <form onSubmit={save} className="mt-1 flex min-w-0 flex-col gap-1.5">
                <Label htmlFor={containsId}>Page must contain</Label>
                <div className="flex min-w-0 items-center gap-2">
                  <Input
                    id={containsId}
                    ref={inputRef}
                    className="min-w-0"
                    value={contains}
                    disabled={pending}
                    maxLength={200}
                    // Mounted by the menu or the saved text, the only time the field is new.
                    autoFocus={editing && contains === m.contains}
                    onChange={(e) => setContains(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key !== 'Escape') return
                      e.preventDefault()
                      cancel()
                    }}
                  />
                  {textEdited && <Button type="submit" size="sm" disabled={pending}>Save</Button>}
                  <Button type="button" size="sm" variant="ghost" onClick={cancel}>Cancel</Button>
                </div>
              </form>
            )}
          </div>}
        </>
      )}
    </li>
  )
}

/**
 * A project's availability monitors: one row per eligible origin, each with its
 * own switch. The domains they share are DomainsSection's.
 */
export default function MonitoringSection({ projectId, pending, onSave, onCheck }: Props) {
  // The checks the page stopped waiting for, by origin: it looks less often,
  // and offers the row's button again, whose press the server answers.
  const [gaveUp, setGaveUp] = useState<Record<string, string>>({})
  const q = useQuery({
    ...monitorsQuery(projectId),
    refetchInterval: (query) => {
      const monitors = query.state.data?.monitors ?? []
      const waiting = monitors.filter((m) => pendingCheck(m) !== null)
      if (waiting.length === 0) return monitors.some((m) => m.enabled) ? STATUS_POLL_MS : false
      return waiting.every((m) => pendingCheck(m) === gaveUp[m.origin]) ? CHECK_SLOW_POLL_MS : CHECK_POLL_MS
    },
  })
  const tabs = useQuery(projectTabsQuery(projectId)).data?.tabs
  const [url] = useSearchParams()
  const data = q.data
  const anyOn = data?.monitors.some((m) => m.enabled) ?? false
  // Re-render once a minute while a monitor is on: a poll that brings the same
  // JSON leaves the data as it was, and the ages would freeze.
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    if (!anyOn) return
    setNow(new Date())
    const timer = setInterval(() => setNow(new Date()), STATUS_POLL_MS)
    return () => clearInterval(timer)
  }, [anyOn])
  const giveUp = useCallback((origin: string, requestedAt: string) => setGaveUp((g) => ({ ...g, [origin]: requestedAt })), [])

  // A refetch that fails over cached monitors keeps the rows (and any unsaved
  // text in them); only a first load that failed replaces the section.
  if (q.isError && !q.data) {
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
  const availability = tabs?.find((t) => t.owner === 'system' && t.title === 'Availability')
  // A refusal may mean a check this page has not heard of, from another tab
  // or the CLI: read the monitors, so it shows.
  const check = (origin: string) => {
    void onCheck(origin).then((asked) => {
      if (!asked) void q.refetch()
    })
  }

  return (
    <section aria-label="Monitoring" className="flex flex-col gap-3 rounded-lg border p-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <h2 className="text-base font-semibold">Monitoring</h2>
        {anyOn && (
          <p className="flex flex-wrap items-baseline gap-x-3 text-sm text-muted-foreground">
            <span>{`each site is checked every ${formatInterval(data.interval_seconds)}`}</span>
            {availability && (
              <Link className="underline underline-offset-4" to={tabPath(projectId, availability.dashboard_id, rangeParams(url).toString())}>
                Open Availability
              </Link>
            )}
          </p>
        )}
      </div>
      {data.paused && <p className="text-sm text-muted-foreground">Monitoring is paused on this install</p>}
      {data.events_per_month > 0 && (
        <p className="text-sm text-muted-foreground">{`Each site uses about ${formatExact(data.events_per_month)} events a month`}</p>
      )}
      {q.isError && (
        <p role="status" className="text-sm text-muted-foreground">
          Couldn't refresh the monitors; what shows may be out of date.{' '}
          <button type="button" className="underline underline-offset-2" onClick={() => void q.refetch()}>Retry</button>
        </p>
      )}
      {!anyOn && <p className="text-sm text-muted-foreground">Check that the site is up, and when its certificate and domain expire.</p>}
      <ul className="flex flex-col gap-5">
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
            paused={data.paused}
          />
        ))}
      </ul>
    </section>
  )
}

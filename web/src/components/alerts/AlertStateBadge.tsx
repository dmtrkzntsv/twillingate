import { BellOffIcon, BellRingIcon, PauseIcon, TriangleAlertIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { breachedSince } from '@/lib/alerts'
import type { Alert } from '@/lib/api'

const sinceFormat = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric' })
const untilFormat = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })

/**
 * Where an alert stands, as a badge: Pending, OK, Breached (since the day
 * its current breach began), No data, Broken, Paused, or Snoozed until when
 * it wakes, in the browser's own time. Breached is red, OK green and Broken
 * amber, as the Inbox colours an alert's notifications.
 */
export default function AlertStateBadge({ alert }: { alert: Alert }) {
  switch (alert.state) {
    case 'ok':
      return (
        <Badge variant="outline" className="text-emerald-600 dark:text-emerald-400">
          OK
        </Badge>
      )
    case 'breached': {
      const since = breachedSince(alert)
      return (
        <Badge variant="outline" className="border-destructive/40 text-destructive">
          <BellRingIcon aria-hidden />
          {since ? `Breached since ${sinceFormat.format(new Date(since))}` : 'Breached'}
        </Badge>
      )
    }
    case 'no_data':
      return (
        <Badge variant="outline" className="text-muted-foreground">
          No data
        </Badge>
      )
    case 'broken':
      return (
        <Badge variant="outline" className="border-amber-600/40 text-amber-600 dark:text-amber-400">
          <TriangleAlertIcon aria-hidden />
          Broken
        </Badge>
      )
    case 'paused':
      return (
        <Badge variant="outline">
          <PauseIcon aria-hidden />
          Paused
        </Badge>
      )
    case 'snoozed':
      return (
        <Badge variant="outline">
          <BellOffIcon aria-hidden />
          {alert.snoozed_until ? `Snoozed until ${untilFormat.format(new Date(alert.snoozed_until))}` : 'Snoozed'}
        </Badge>
      )
    case 'pending':
      return <Badge variant="outline">Pending</Badge>
    default:
      // A state this console does not know yet (a newer server's) still shows, as it is named.
      return <Badge variant="outline">{String(alert.state)}</Badge>
  }
}

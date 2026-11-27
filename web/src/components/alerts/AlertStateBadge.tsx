import { BellOffIcon, BellRingIcon, PauseIcon, TriangleAlertIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { breachedSince, stateLabel } from '@/lib/alerts'
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
          {stateLabel('ok')}
        </Badge>
      )
    case 'breached': {
      const since = breachedSince(alert)
      return (
        <Badge variant="outline" className="border-destructive/40 text-destructive">
          <BellRingIcon aria-hidden />
          {since ? `${stateLabel('breached')} since ${sinceFormat.format(new Date(since))}` : stateLabel('breached')}
        </Badge>
      )
    }
    case 'no_data':
      return (
        <Badge variant="outline" className="text-muted-foreground">
          {stateLabel('no_data')}
        </Badge>
      )
    case 'broken':
      return (
        <Badge variant="outline" className="border-amber-600/40 text-amber-600 dark:text-amber-400">
          <TriangleAlertIcon aria-hidden />
          {stateLabel('broken')}
        </Badge>
      )
    case 'paused':
      return (
        <Badge variant="outline">
          <PauseIcon aria-hidden />
          {stateLabel('paused')}
        </Badge>
      )
    case 'snoozed':
      return (
        <Badge variant="outline">
          <BellOffIcon aria-hidden />
          {alert.snoozed_until ? `${stateLabel('snoozed')} until ${untilFormat.format(new Date(alert.snoozed_until))}` : stateLabel('snoozed')}
        </Badge>
      )
    case 'pending':
      return <Badge variant="outline">{stateLabel('pending')}</Badge>
    default:
      // A state this console does not know yet (a newer server's) still shows, as it is named.
      return <Badge variant="outline">{stateLabel(alert.state)}</Badge>
  }
}

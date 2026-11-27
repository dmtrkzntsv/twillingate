import { ArchiveIcon, ArchiveRestoreIcon, BellIcon, BellOffIcon, ChevronDownIcon, PauseIcon, PlayIcon, RefreshCwIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { AlertActions as Actions } from '@/hooks/use-alert-actions'
import { checkBlocked } from '@/lib/alerts'
import type { Alert } from '@/lib/api'

const SNOOZES = [
  { choice: '1h', label: '1 hour' },
  { choice: '1d', label: '1 day' },
  { choice: '1w', label: '1 week' },
] as const

/**
 * What can be done to an alert, in a row that wraps: **Check now**
 * (`Checking…` while a check is pending; kept from a paused or snoozed
 * alert, or one of an archived project, which the server would refuse,
 * with a tooltip saying why),
 * **Pause** or **Resume**, a **Snooze** menu (not while paused) or **Wake**
 * while snoozed, and **Archive**. An archived alert has only **Restore**.
 * Archive and Restore ask nothing first: each undoes the other.
 */
export default function AlertActions({ alert, actions, projectArchived = false }: { alert: Alert; actions: Actions; projectArchived?: boolean }) {
  const { id } = alert
  if (alert.archived_at !== '') {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" variant="outline" size="sm" disabled={actions.pending} onClick={() => void actions.restore(id)}>
          <ArchiveRestoreIcon />
          Restore
        </Button>
      </div>
    )
  }
  const blocked = checkBlocked(alert, projectArchived)
  const checking = alert.check_pending
  const idle = blocked !== null || checking || actions.pending
  const check = (
    // aria-disabled rather than disabled, so it keeps its focus and its tooltip.
    <Button
      type="button"
      variant="outline"
      size="sm"
      className="aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
      aria-disabled={idle || undefined}
      onClick={idle ? undefined : () => void actions.check(id)}
    >
      <RefreshCwIcon className={checking ? 'animate-spin motion-reduce:animate-none' : undefined} />
      {checking ? 'Checking…' : 'Check now'}
    </Button>
  )
  const snoozed = alert.state === 'snoozed'
  return (
    <div className="flex flex-wrap items-center gap-2">
      {blocked ? (
        <Tooltip>
          <TooltipTrigger asChild>{check}</TooltipTrigger>
          <TooltipContent>{blocked}</TooltipContent>
        </Tooltip>
      ) : (
        check
      )}
      {alert.paused ? (
        <Button type="button" variant="outline" size="sm" disabled={actions.pending} onClick={() => void actions.resume(id)}>
          <PlayIcon />
          Resume
        </Button>
      ) : (
        <Button type="button" variant="outline" size="sm" disabled={actions.pending} onClick={() => void actions.pause(id)}>
          <PauseIcon />
          Pause
        </Button>
      )}
      {snoozed ? (
        <Button type="button" variant="outline" size="sm" disabled={actions.pending} onClick={() => void actions.wake(id)}>
          <BellIcon />
          Wake
        </Button>
      ) : (
        !alert.paused && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button type="button" variant="outline" size="sm" disabled={actions.pending}>
                <BellOffIcon />
                Snooze
                <ChevronDownIcon />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              {SNOOZES.map((s) => (
                <DropdownMenuItem key={s.choice} onClick={() => void actions.snooze(id, s.choice)}>
                  {s.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )
      )}
      <Button type="button" variant="outline" size="sm" disabled={actions.pending} onClick={() => void actions.archive(id)}>
        <ArchiveIcon />
        Archive
      </Button>
    </div>
  )
}

import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router'
import { linkTab } from '@/components/projects/SettingsTabs'
import { ALERTS_ID, tabPath } from '@/lib/project-tabs'
import { alertsQuery } from '@/lib/queries'

interface Props {
  projectId: number
  /** The alert on screen (`/alerts/:alertId`); none on the list. */
  alertId?: string
  /** The page's range, kept across the tabs. */
  search: string
}

/** The list of a project's alerts, the archived ones with `show=archived`, keeping the range. */
function listPath(projectId: number, archived: boolean, search: string): string {
  if (!archived) return tabPath(projectId, ALERTS_ID, search)
  return tabPath(projectId, ALERTS_ID, search ? `show=archived&${search}` : 'show=archived')
}

/**
 * The Alerts page's tab row: Live and Archived, the project's two lists of
 * alerts, as links shown as tabs (as `SettingsTabs` are), so each always
 * opens its list, the current one included from an alert's page. Two short
 * words, they fit a phone's row as they are. On a list the current tab is
 * the one `?show` picks; on an alert's page it is the list the alert is
 * in, read from the lists the alert's page reads (their cache shared): Live
 * while the live list holds it, Archived once the archived list (read only
 * when the live one lacks it) does, and none while they load or when
 * neither holds it (a purged alert, a wrong link).
 */
export default function AlertTabs({ projectId, alertId, search }: Props) {
  const [url] = useSearchParams()
  const onAlert = alertId !== undefined
  // Only an alert's page asks: on a list, that list's own query is the one to read.
  const live = useQuery({ ...alertsQuery(projectId, false), enabled: onAlert })
  const inLive = live.data?.alerts.some((a) => String(a.id) === alertId) ?? false
  const archived = useQuery({ ...alertsQuery(projectId, true), enabled: onAlert && live.data !== undefined && !inLive })
  let current: 'live' | 'archived' | null
  if (!onAlert) current = url.get('show') === 'archived' ? 'archived' : 'live'
  else if (inLive) current = 'live'
  else if (archived.data?.alerts.some((a) => String(a.id) === alertId)) current = 'archived'
  else current = null

  const tabs = [
    { id: 'live', title: 'Live', to: listPath(projectId, false, search) },
    { id: 'archived', title: 'Archived', to: listPath(projectId, true, search) },
  ] as const
  return (
    <nav aria-label="Alert lists" className="flex h-full min-w-0 items-center overflow-x-auto overflow-y-hidden [scrollbar-width:none]">
      <ul className="inline-flex h-9 w-max items-center gap-1 p-[3px]">
        {tabs.map((t) => (
          <li key={t.id} className="h-full">
            <Link to={t.to} className={linkTab} aria-current={t.id === current ? 'page' : undefined}>
              {t.title}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  )
}

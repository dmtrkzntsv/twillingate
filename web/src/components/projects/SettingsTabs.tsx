import { Link, useNavigate } from 'react-router'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { settingsPath, settingsSections, type SettingsSection } from '@/lib/project-tabs'

/** A dashboard tab's look (`ReportTabs`, the line variant): muted until current, then underlined in the primary colour on the row's border. */
const tab =
  'relative inline-flex h-[calc(100%-1px)] flex-none items-center rounded-md border border-transparent px-3 py-1 text-sm font-medium whitespace-nowrap text-foreground/60 transition-all hover:text-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-1 focus-visible:outline-ring dark:text-muted-foreground dark:hover:text-foreground ' +
  'after:absolute after:inset-x-0 after:bottom-[-5px] after:h-0.5 after:rounded-full after:bg-primary after:opacity-0 after:transition-opacity aria-[current=page]:text-foreground aria-[current=page]:after:opacity-100'

interface Props {
  projectId: number
  archived: boolean
  /** The tab on screen. */
  current: SettingsSection
  /** The range part of the URL, which every tab keeps (D8). */
  search: string
}

/**
 * A project's Settings tabs (project settings tabs), in the tab row where
 * its dashboards are otherwise, and shown as they are (`ReportTabs`): on
 * wide screens a row of links, so each tab has its address, that scrolls
 * sideways when it runs out of room; on phones a select that opens the
 * same addresses, since nothing may scroll sideways there.
 */
export default function SettingsTabs({ projectId, archived, current, search }: Props) {
  const navigate = useNavigate()
  const sections = settingsSections(archived)
  return (
    <>
      <Select value={current} onValueChange={(v) => navigate(settingsPath(projectId, v as SettingsSection, search))}>
        <SelectTrigger size="sm" aria-label="Settings section" className="min-w-32 sm:hidden">
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper" align="start">
          {sections.map((s) => (
            <SelectItem key={s.id} value={s.id}>
              {s.title}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <nav aria-label="Settings sections" className="hidden h-full min-w-0 items-center overflow-x-auto overflow-y-hidden [scrollbar-width:none] sm:flex">
        <ul className="inline-flex h-9 w-max items-center gap-1 p-[3px]">
          {sections.map((s) => (
            <li key={s.id} className="h-full">
              <Link to={settingsPath(projectId, s.id, search)} className={tab} aria-current={s.id === current ? 'page' : undefined}>
                {s.title}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
    </>
  )
}

import { Link } from 'react-router'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import type { IngestKey, Project, ProjectActivity, ProjectUsage } from '@/lib/api'
import { formatValue } from '@/lib/format'
import { formsNeedAttention } from '@/lib/forms'
import { FORMS_ID, tabPath } from '@/lib/project-tabs'
import { formatAgo } from '@/lib/units'
import { cn } from '@/lib/utils'
import Sparkline from './Sparkline'

const DAY = 86_400_000

interface Props {
  project: Project
  /** Undefined while stats load. */
  stats?: ProjectUsage
  /** Undefined while keys load. */
  keys?: IngestKey[]
  /** The stats read failed (the page says why): no skeleton, which would never resolve. */
  statsFailed?: boolean
  /** Undefined while activity loads, and in reporting dev. */
  activity?: ProjectActivity
}

/**
 * One project at a glance: freshness, 30 days of events and keys;
 * and, when it has forms, how many, the drafts among them and
 * the submissions not read yet, a link to its Forms tab.
 */
export default function ProjectCard({ project, stats, keys, statsFailed, activity }: Props) {
  const last = stats?.last_received_at
  const live = last !== null && last !== undefined && Date.now() - new Date(last).getTime() < DAY
  const total = stats ? stats.totals.views + stats.totals.events + stats.totals.measures : undefined
  const active = keys?.filter((k) => k.state === 'active').length
  return (
    <Card role="article" aria-label={project.name} className="relative gap-3 py-4 transition-colors hover:border-primary/40">
      <CardHeader className="flex flex-row items-start justify-between gap-2 px-4">
        <CardTitle className="truncate text-base">
          <Link to={`/projects/${project.project_id}`} className="underline-offset-2 after:absolute after:inset-0 hover:underline">{project.name}</Link>
        </CardTitle>
        <span className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
          <span className={cn('size-2 rounded-full', live ? 'bg-emerald-500' : 'bg-muted-foreground/40')} />
          {!stats ? '—' : last ? formatAgo(last) : 'Nothing received yet'}
        </span>
      </CardHeader>
      <CardContent className="flex flex-col gap-2 px-4">
        {stats ? <Sparkline series={stats.series} /> : !statsFailed && <Skeleton className="h-10 w-full" />}
        <p className="text-sm">
          <span className="font-medium">{total === undefined ? '—' : `${formatValue(total)} events`}</span>
          <span className="text-muted-foreground"> · 30 days</span>
        </p>
        <p className="text-xs text-muted-foreground">
          {active === undefined ? '—' : `${active} active ${active === 1 ? 'key' : 'keys'}`}
        </p>
        {activity && activity.forms > 0 && <FormsLine projectId={project.project_id} activity={activity} />}
      </CardContent>
    </Card>
  )
}

/** "2 forms · 1 draft · 3 new submissions", the parts that wait on someone emphasized; above the card's own link. */
function FormsLine({ projectId, activity }: { projectId: number; activity: ProjectActivity }) {
  const { forms, draft_forms: drafts, new_submissions: fresh } = activity
  const parts: { text: string; attention: boolean }[] = [
    { text: `${formatValue(forms)} ${forms === 1 ? 'form' : 'forms'}`, attention: false },
  ]
  if (drafts > 0) parts.push({ text: `${formatValue(drafts)} ${drafts === 1 ? 'draft' : 'drafts'}`, attention: true })
  if (fresh > 0) parts.push({ text: `${formatValue(fresh)} new ${fresh === 1 ? 'submission' : 'submissions'}`, attention: true })
  return (
    <Link
      to={tabPath(projectId, FORMS_ID)}
      aria-label={parts.map((p) => p.text).join(' · ')}
      className="relative z-10 flex items-center gap-1.5 self-start text-xs text-muted-foreground underline-offset-2 hover:underline"
    >
      {formsNeedAttention(activity) && <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-primary" />}
      <span>
        {parts.map((p, i) => (
          <span key={p.text}>
            {i > 0 && ' · '}
            <span className={cn(p.attention && 'font-medium text-foreground')}>{p.text}</span>
          </span>
        ))}
      </span>
    </Link>
  )
}

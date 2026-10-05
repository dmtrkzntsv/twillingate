import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import type { IngestKey, Project, ProjectUsage } from '@/lib/api'
import { formatValue } from '@/lib/format'
import { formatAgo, formatBytes } from '@/lib/units'
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
  onRestore?: () => void
  pending?: boolean
}

/** One project at a glance: freshness, 30 days of events, size, keys and attributes. */
export default function ProjectCard({ project, stats, keys, statsFailed, onRestore, pending }: Props) {
  const last = stats?.last_received_at
  const live = last !== null && last !== undefined && Date.now() - new Date(last).getTime() < DAY
  const total = stats ? stats.totals.views + stats.totals.events + stats.totals.measures : undefined
  const active = keys?.filter((k) => k.state === 'active').length
  const attrs = project.attributes?.length ?? 0
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
          {[
            stats ? (stats.size ? formatBytes(stats.size.total_bytes) : 'size not measured yet') : '—',
            active === undefined ? '—' : `${active} active ${active === 1 ? 'key' : 'keys'}`,
            `${attrs} ${attrs === 1 ? 'attribute' : 'attributes'}`,
          ].join(' · ')}
        </p>
        {onRestore && (
          <Button variant="outline" size="sm" className="relative z-10 self-start" disabled={pending} onClick={onRestore}>
            Restore
          </Button>
        )}
      </CardContent>
    </Card>
  )
}

import { useQuery } from '@tanstack/react-query'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { MonitorDomain } from '@/lib/api'
import { plural } from '@/lib/format'
import { monitorsQuery } from '@/lib/queries'
import { daysUntilDate, formatDaysLeft } from '@/lib/time'

/** Soonest renewal first; a domain whose date is unknown last. */
function byRenewal(a: MonitorDomain, b: MonitorDomain): number {
  if (a.expires_at === b.expires_at) return a.domain.localeCompare(b.domain)
  if (!a.expires_at) return 1
  if (!b.expires_at) return -1
  return a.expires_at < b.expires_at ? -1 : 1
}

/**
 * The main domains a project's monitors share, with when each renews. Shown
 * only once there is a reading; it reads the monitors' own query.
 */
export default function DomainsSection({ projectId }: { projectId: number }) {
  const domains = useQuery(monitorsQuery(projectId)).data?.domains ?? []
  if (domains.length === 0) return null
  const now = new Date()
  return (
    <section aria-label="Domains" className="flex flex-col gap-3 rounded-lg border p-4">
      <h2 className="text-base font-semibold">Domains</h2>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Domain</TableHead>
            <TableHead>Renews</TableHead>
            <TableHead>Left</TableHead>
            <TableHead className="hidden sm:table-cell">Sites</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {[...domains].sort(byRenewal).map((d) => {
            const days = d.expires_at ? daysUntilDate(d.expires_at, now) : null
            return (
              <TableRow key={d.domain} aria-label={d.domain}>
                <TableCell className="min-w-0 break-all whitespace-normal">{d.domain}</TableCell>
                <TableCell className="text-muted-foreground">{d.expires_at ? (<><span className="hidden sm:inline">renews </span>{d.expires_at}</>) : 'unknown'}</TableCell>
                <TableCell className={days !== null && days < 0 ? 'text-destructive' : 'text-muted-foreground'}>
                  {days === null ? '' : formatDaysLeft(days)}
                </TableCell>
                <TableCell className="hidden text-muted-foreground sm:table-cell">{plural(d.origins.length, 'site')}</TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </section>
  )
}

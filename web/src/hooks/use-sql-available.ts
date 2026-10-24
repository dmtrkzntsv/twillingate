import { useQuery } from '@tanstack/react-query'
import { componentsQuery } from '@/lib/queries'

/**
 * Whether the server takes SQL sources (an external SQL engine is
 * configured), and whether that is known yet. While the components load,
 * and if they fail to, SQL counts as available: a server with an engine
 * never sees SQL flicker away, and a failed read leaves each component's
 * own accepts in charge.
 */
export function useSqlAvailable(): { sql: boolean; known: boolean } {
  const q = useQuery(componentsQuery)
  if (q.data) return { sql: q.data.source_types.includes('sql'), known: true }
  return { sql: true, known: q.isError }
}

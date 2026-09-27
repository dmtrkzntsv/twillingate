import { useEffect, useRef } from 'react'
import { useQuery } from '@tanstack/react-query'
import { endpoints } from '@/lib/api'

/**
 * In `reporting dev`, polls the served files' version every 500ms and
 * reloads the page when it changes, so an edited dashboard shows at once.
 */
export function useDevReload(enabled: boolean): void {
  const { data } = useQuery({
    queryKey: ['dev-version'],
    queryFn: endpoints.devVersion,
    enabled,
    refetchInterval: 500,
    refetchIntervalInBackground: true,
    gcTime: 0,
  })
  const first = useRef<string | undefined>(undefined)

  useEffect(() => {
    if (!data) return
    if (first.current === undefined) first.current = data.version
    else if (data.version !== first.current) window.location.reload()
  }, [data])
}

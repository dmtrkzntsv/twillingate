import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { endpoints } from '@/lib/api'

/**
 * Marks a form read up to `until` (project landing D5), then refreshes the
 * forms list and the activity counts. Console state: a failure is silent;
 * the badge stays until the next visit marks it.
 */
export function useMarkFormSeen() {
  const queryClient = useQueryClient()
  return useCallback(
    (projectId: number, name: string, until: string) => {
      endpoints
        .markFormSeen(projectId, name, until)
        .then(() =>
          Promise.all([
            queryClient.invalidateQueries({ queryKey: ['forms', projectId] }),
            queryClient.invalidateQueries({ queryKey: ['project-activity'] }),
          ])
        )
        .catch(() => {})
    },
    [queryClient]
  )
}

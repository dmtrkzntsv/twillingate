import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type ArchiveAfter, type WidgetShare } from '@/lib/api'

export interface WidgetShareActions {
  /** True while an action runs; buttons wait on it. */
  pending: boolean
  create(form: FormData): Promise<WidgetShare | undefined>
  setArchiveAfter(id: string, v: ArchiveAfter): Promise<WidgetShare | undefined>
  archive(id: string): Promise<WidgetShare | undefined>
  restore(id: string, v: ArchiveAfter): Promise<WidgetShare | undefined>
}

/**
 * Every widget share write, each the existing audited route. A success
 * toasts and refetches every share list; a failure toasts its message,
 * refetches nothing and resolves undefined instead of throwing.
 */
export function useWidgetShareActions(): WidgetShareActions {
  const client = useQueryClient()
  const [pending, setPending] = useState(false)

  const run = useCallback(
    async (fn: () => Promise<WidgetShare>, done: string): Promise<WidgetShare | undefined> => {
      setPending(true)
      try {
        const out = await fn()
        toast.success(done)
        await client.invalidateQueries({ queryKey: ['widget-shares'] })
        return out
      } catch (err) {
        if (err instanceof ApiError) toast.error(err.message)
        else if (err instanceof TypeError) toast.error("Couldn't reach the server")
        else toast.error(err instanceof Error ? err.message : String(err))
        return undefined
      } finally {
        setPending(false)
      }
    },
    [client]
  )

  return {
    pending,
    create: (form) => run(() => endpoints.createWidgetShare(form), 'Link created'),
    setArchiveAfter: (id, v) => run(() => endpoints.updateWidgetShare(id, v), 'Archive date changed'),
    archive: (id) => run(() => endpoints.archiveWidgetShare(id), 'Share archived'),
    restore: (id, v) => run(() => endpoints.restoreWidgetShare(id, v), 'Share restored'),
  }
}

import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { endpoints, type ArchiveAfter, type WidgetShare } from '@/lib/api'
import { toastError } from '@/lib/toast-error'

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
 * toasts (but create, whose button says so) and refetches every share list;
 * a failure toasts its message, refetches nothing and resolves undefined
 * instead of throwing.
 */
export function useWidgetShareActions(): WidgetShareActions {
  const client = useQueryClient()
  const [pending, setPending] = useState(false)

  const run = useCallback(
    async (fn: () => Promise<WidgetShare>, done?: string): Promise<WidgetShare | undefined> => {
      setPending(true)
      try {
        const out = await fn()
        if (done) toast.success(done)
        await client.invalidateQueries({ queryKey: ['widget-shares'] })
        return out
      } catch (err) {
        toastError(err)
        return undefined
      } finally {
        setPending(false)
      }
    },
    [client]
  )

  return {
    pending,
    create: (form) => run(() => endpoints.createWidgetShare(form)),
    setArchiveAfter: (id, v) => run(() => endpoints.updateWidgetShare(id, v), 'Archive date changed'),
    archive: (id) => run(() => endpoints.archiveWidgetShare(id), 'Share archived'),
    restore: (id, v) => run(() => endpoints.restoreWidgetShare(id, v), 'Share restored'),
  }
}

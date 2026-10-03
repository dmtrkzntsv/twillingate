import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type CreateProjectBody, type CreatedProject, type IssuedKey } from '@/lib/api'

export interface ProjectActions {
  create(body: CreateProjectBody): Promise<CreatedProject | undefined>
  update(id: number, body: Partial<CreateProjectBody>): Promise<boolean>
  archive(id: number): Promise<boolean | undefined>
  restore(id: number): Promise<boolean | undefined>
  issueKey(id: number, label: string): Promise<IssuedKey | undefined>
  disableKey(id: number, label: string): Promise<boolean | undefined>
  enableKey(id: number, label: string): Promise<boolean | undefined>
  /** True while an action runs; buttons wait on it. */
  pending: boolean
}

/**
 * Every project and key write, each the existing audited route. A success
 * toasts and refetches projects, keys, stats and cap usage, so the
 * sidebar, the list and the dashboards' project switcher follow; a
 * failure toasts its message and resolves undefined (false for update)
 * instead of throwing.
 */
export function useProjectActions(): ProjectActions {
  const client = useQueryClient()
  const [pending, setPending] = useState(false)

  const run = useCallback(
    async <T,>(fn: () => Promise<T>, done: string): Promise<T | undefined> => {
      setPending(true)
      try {
        const out = await fn()
        toast.success(done)
        return out
      } catch (err) {
        if (err instanceof ApiError) toast.error(err.message)
        else if (err instanceof TypeError) toast.error("Couldn't reach the server")
        else toast.error(err instanceof Error ? err.message : String(err))
        return undefined
      } finally {
        for (const key of ['projects', 'keys', 'stats', 'cap-usage']) void client.invalidateQueries({ queryKey: [key] })
        setPending(false)
      }
    },
    [client]
  )

  return {
    pending,
    create: (body) => run(() => endpoints.createProject(body), `Created ${body.name}`),
    update: async (id, body) => (await run(() => endpoints.updateProject(id, body), 'Saved')) !== undefined,
    archive: (id) => run(async () => (await endpoints.archiveProject(id), true), 'Archived'),
    restore: (id) => run(async () => (await endpoints.restoreProject(id), true), 'Restored'),
    issueKey: (id, label) => run(() => endpoints.issueKey(id, label), `Issued ${label}`),
    disableKey: (id, label) => run(async () => (await endpoints.disableKey(id, label), true), `Disabled ${label}`),
    enableKey: (id, label) => run(async () => (await endpoints.enableKey(id, label), true), `Enabled ${label}`),
  }
}

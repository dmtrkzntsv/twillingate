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

/** What a project write changes: the list, its keys, and the usage cards. */
const PROJECT_WRITE = ['projects', 'keys', 'stats']
/** What a key write changes. */
const KEY_WRITE = ['keys']

/**
 * Every project and key write, each the existing audited route. A success
 * toasts and refetches what it changed, so the sidebar, the list and the
 * dashboards' project switcher follow: a project write refetches projects,
 * keys and stats (a new or archived project changes the list's cards and
 * the database size), a key write refetches keys. Cap usage never changes
 * with a management action. A failure toasts its message, refetches
 * nothing, and resolves undefined (false for update) instead of throwing.
 */
export function useProjectActions(): ProjectActions {
  const client = useQueryClient()
  const [pending, setPending] = useState(false)

  const run = useCallback(
    async <T,>(fn: () => Promise<T>, done: string, changed: string[]): Promise<T | undefined> => {
      setPending(true)
      try {
        const out = await fn()
        toast.success(done)
        for (const key of changed) void client.invalidateQueries({ queryKey: [key] })
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
    create: (body) => run(() => endpoints.createProject(body), `Created ${body.name}`, PROJECT_WRITE),
    update: async (id, body) => (await run(() => endpoints.updateProject(id, body), 'Saved', PROJECT_WRITE)) !== undefined,
    archive: (id) => run(async () => (await endpoints.archiveProject(id), true), 'Archived', PROJECT_WRITE),
    restore: (id) => run(async () => (await endpoints.restoreProject(id), true), 'Restored', PROJECT_WRITE),
    issueKey: (id, label) => run(() => endpoints.issueKey(id, label), `Issued ${label}`, KEY_WRITE),
    disableKey: (id, label) => run(async () => (await endpoints.disableKey(id, label), true), `Disabled ${label}`, KEY_WRITE),
    enableKey: (id, label) => run(async () => (await endpoints.enableKey(id, label), true), `Enabled ${label}`, KEY_WRITE),
  }
}

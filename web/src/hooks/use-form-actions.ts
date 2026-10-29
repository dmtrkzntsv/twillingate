import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type FormUpdate, type SubmissionsSelector } from '@/lib/api'

export interface FormActions {
  approve(projectId: number, name: string, fields: string[]): Promise<boolean>
  update(projectId: number, name: string, body: FormUpdate, done?: string): Promise<boolean>
  archive(projectId: number, name: string): Promise<boolean>
  restore(projectId: number, name: string): Promise<boolean>
  /** Resolves how many were archived, undefined on a refusal. */
  archiveSubmissions(projectId: number, body: SubmissionsSelector): Promise<number | undefined>
  /** Resolves how many were restored, undefined on a refusal. */
  restoreSubmissions(projectId: number, body: SubmissionsSelector): Promise<number | undefined>
  /** True while an action runs; buttons wait on it. */
  pending: boolean
}

/**
 * What a form write changes: the lists (and their counts), the tables, Find
 * a person, the Forms badge (project activity) and an open submission (its
 * archived_at).
 */
const FORM_WRITE = ['forms', 'submissions', 'submission', 'find-submissions', 'project-activity']

/**
 * Every form write, each the audited route, as `useProjectActions` does
 * them: a success toasts and refetches the forms, their tables and Find a
 * person; a failure toasts its message and resolves false (undefined for
 * an archive or restore of submissions) instead of throwing.
 */
export function useFormActions(): FormActions {
  const client = useQueryClient()
  const [pending, setPending] = useState(false)

  const run = useCallback(
    async <T,>(fn: () => Promise<T>, done: (out: T) => string): Promise<T | undefined> => {
      setPending(true)
      try {
        const out = await fn()
        toast.success(done(out))
        await Promise.all(FORM_WRITE.map((key) => client.invalidateQueries({ queryKey: [key] })))
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
  const ok = (out: unknown) => out !== undefined

  return {
    pending,
    approve: async (projectId, name, fields) =>
      ok(await run(() => endpoints.approveForm(projectId, name, fields), () => `Approved ${name}`)),
    update: async (projectId, name, body, done = 'Saved') => ok(await run(() => endpoints.updateForm(projectId, name, body), () => done)),
    archive: async (projectId, name) =>
      ok(await run(() => endpoints.archiveForm(projectId, name), () => `Archived ${name}; restore it from the Archive page`)),
    restore: async (projectId, name) => ok(await run(() => endpoints.restoreForm(projectId, name), () => `Restored ${name}`)),
    archiveSubmissions: async (projectId, body) =>
      (
        await run(
          () => endpoints.archiveSubmissions(projectId, body),
          (out) => `Archived ${out.archived} ${out.archived === 1 ? 'submission' : 'submissions'}`
        )
      )?.archived,
    restoreSubmissions: async (projectId, body) =>
      (
        await run(
          () => endpoints.restoreSubmissions(projectId, body),
          (out) => `Restored ${out.restored} ${out.restored === 1 ? 'submission' : 'submissions'}`
        )
      )?.restored,
  }
}

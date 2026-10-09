import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type DeleteSubmissionsBody, type FormUpdate } from '@/lib/api'

export interface FormActions {
  approve(projectId: number, name: string, fields: string[]): Promise<boolean>
  update(projectId: number, name: string, body: FormUpdate, done?: string): Promise<boolean>
  archive(projectId: number, name: string): Promise<boolean>
  restore(projectId: number, name: string): Promise<boolean>
  /** Resolves how many were deleted, undefined on a refusal. */
  deleteSubmissions(projectId: number, body: DeleteSubmissionsBody): Promise<number | undefined>
  /** True while an action runs; buttons wait on it. */
  pending: boolean
}

/**
 * What a form write changes: the lists (and their counts), the tables, Find
 * a person and the Forms badge (project activity). A stored submission
 * never changes, and refetching one just deleted would only fail, so the
 * drawer's query is left alone.
 */
const FORM_WRITE = ['forms', 'submissions', 'find-submissions', 'project-activity']

/**
 * Every form write, each the audited route, as `useProjectActions` does
 * them: a success toasts and refetches the forms, their tables and Find a
 * person; a failure toasts its message and resolves false (undefined for
 * a delete) instead of throwing.
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
    deleteSubmissions: async (projectId, body) =>
      (
        await run(
          () => endpoints.deleteSubmissions(projectId, body),
          (out) => `Deleted ${out.deleted} ${out.deleted === 1 ? 'submission' : 'submissions'}`
        )
      )?.deleted,
  }
}

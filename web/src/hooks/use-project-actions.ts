import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type CreateProjectBody, type CreatedProject, type EventFamily, type IssuedKey } from '@/lib/api'
import { toastError } from '@/lib/toast-error'

export interface ProjectActions {
  create(body: CreateProjectBody): Promise<CreatedProject | undefined>
  update(id: number, body: Partial<CreateProjectBody>): Promise<boolean>
  archive(id: number): Promise<boolean | undefined>
  restore(id: number): Promise<boolean | undefined>
  issueKey(id: number, label: string): Promise<IssuedKey | undefined>
  disableKey(id: number, label: string): Promise<boolean | undefined>
  enableKey(id: number, label: string): Promise<boolean | undefined>
  /**
   * Renames an event as it arrives and in what is stored; resolves true when done. A 409 (the new name already has
   * events) is not toasted: it resolves 'conflict' so the caller can point to merging.
   */
  renameEvent(id: number, body: { family: EventFamily; from: string; to: string }): Promise<boolean | 'conflict'>
  /** Merges event `from` into event `to`, as it arrives and in what is stored; resolves true when done. */
  mergeEvent(id: number, body: { family: EventFamily; from: string; to: string }): Promise<boolean>
  /** Drops an event as it arrives and hides its stored events from every read. */
  blockEvent(id: number, body: { family: EventFamily; name: string }): Promise<boolean>
  /** Removes an event's rename or block rule. */
  removeEventRule(id: number, body: { family: EventFamily; name: string }): Promise<boolean>
  /** Maps `alias` to the event `to`: a merge, so the alias's stored events (if any) join it. */
  addEventAlias(id: number, body: { family: EventFamily; alias: string; to: string }): Promise<boolean>
  /**
   * Renames an attribute key as it arrives and in what is stored; resolves true when done. A 409 (the new key already
   * has data, or is blocked) is not toasted: it resolves 'conflict' so the caller can point to merging.
   */
  renameAttribute(id: number, body: { from: string; to: string }): Promise<boolean | 'conflict'>
  /** Merges key `from` into key `to`, as it arrives and in what is stored; resolves true when done. */
  mergeAttribute(id: number, body: { from: string; to: string }): Promise<boolean>
  /** Strips a key from events as they arrive and hides its stored values from every read. */
  blockAttribute(id: number, body: { key: string }): Promise<boolean>
  /** Removes a key's rename or block rule. */
  removeAttributeRule(id: number, body: { key: string }): Promise<boolean>
  /** Maps `alias` to the key `to`: a merge, so the alias's stored values (if any) join it. */
  addAttributeAlias(id: number, body: { alias: string; to: string }): Promise<boolean>
  /** Puts project `id` right after project `after` (0 first); no toast unless refused. Resolves true once the list has the new order. */
  move(id: number, after: number): Promise<boolean>
  /** True while an action runs; buttons wait on it. */
  pending: boolean
}

/** What a project write changes: the list, its keys, and the usage cards. */
const PROJECT_WRITE = ['projects', 'keys', 'usage']
/** What an event rule changes: the rules, the names listed, and the usage counts. */
const EVENT_WRITE = ['event-rules', 'event-names', 'usage']
/** What an attribute rule changes: the attributes listed, and the usage counts. */
const ATTRIBUTE_WRITE = ['attributes', 'usage']
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
    async <T, R = never>(fn: () => Promise<T>, done: string, changed: string[], refused?: (err: unknown) => R | undefined): Promise<T | R | undefined> => {
      setPending(true)
      try {
        const out = await fn()
        toast.success(done)
        const refetches = changed.map((key) => client.invalidateQueries({ queryKey: [key] }))
        // Pending holds until the project list has refetched: an origins
        // save sends the whole list, so a second write in that gap would
        // build on the old list and undo the first.
        if (changed.includes('projects')) await refetches[changed.indexOf('projects')]
        return out
      } catch (err) {
        // A refusal the caller handles itself is handed back instead of toasted.
        const handled = refused?.(err)
        if (handled !== undefined) return handled
        toastError(err)
        return undefined
      } finally {
        setPending(false)
      }
    },
    [client]
  )

  // A drag's drop: the dropped order shows (useReorder) until the list
  // refetches, so a success says nothing, and pending stays off, leaving
  // the page's buttons alone.
  const move = useCallback(
    async (id: number, after: number) => {
      try {
        await endpoints.moveProject(id, after)
      } catch (err) {
        toastError(err)
        return false
      }
      await client.invalidateQueries({ queryKey: ['projects'] })
      return true
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
    renameEvent: async (id, body) => {
      const out = await run(
        () => endpoints.renameEvent(id, body),
        `Renamed ${body.from} to ${body.to}`,
        EVENT_WRITE,
        (err) => (err instanceof ApiError && err.status === 409 ? ('conflict' as const) : undefined)
      )
      return out === 'conflict' ? out : out !== undefined
    },
    mergeEvent: async (id, body) =>
      (await run(() => endpoints.mergeEvent(id, body), `Merged ${body.from} into ${body.to}`, EVENT_WRITE)) !== undefined,
    blockEvent: async (id, body) => (await run(() => endpoints.blockEvent(id, body), `Blocked ${body.name}`, EVENT_WRITE)) !== undefined,
    removeEventRule: async (id, body) =>
      (await run(() => endpoints.removeEventRule(id, body), `Removed the rule for ${body.name}`, EVENT_WRITE)) !== undefined,
    addEventAlias: async (id, { family, alias, to }) =>
      (await run(() => endpoints.mergeEvent(id, { family, from: alias, to }), `Added alias ${alias} to ${to}`, EVENT_WRITE)) !== undefined,
    renameAttribute: async (id, body) => {
      const out = await run(
        () => endpoints.renameAttribute(id, body),
        `Renamed ${body.from} to ${body.to}`,
        ATTRIBUTE_WRITE,
        (err) => (err instanceof ApiError && err.status === 409 ? ('conflict' as const) : undefined)
      )
      return out === 'conflict' ? out : out !== undefined
    },
    mergeAttribute: async (id, body) =>
      (await run(() => endpoints.mergeAttribute(id, body), `Merged ${body.from} into ${body.to}`, ATTRIBUTE_WRITE)) !== undefined,
    blockAttribute: async (id, body) => (await run(() => endpoints.blockAttribute(id, body), `Blocked ${body.key}`, ATTRIBUTE_WRITE)) !== undefined,
    removeAttributeRule: async (id, body) =>
      (await run(() => endpoints.removeAttributeRule(id, body), `Removed the rule for ${body.key}`, ATTRIBUTE_WRITE)) !== undefined,
    addAttributeAlias: async (id, { alias, to }) =>
      (await run(() => endpoints.mergeAttribute(id, { from: alias, to }), `Added alias ${alias} to ${to}`, ATTRIBUTE_WRITE)) !== undefined,
    move,
  }
}

import { act, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, endpoints, type DashboardDetail, type ProjectTab } from '@/lib/api'
import { tabPath } from '@/lib/project-tabs'
import { useProjectTabActions } from './use-project-tab-actions'

const navigate = vi.fn()

vi.mock('react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('react-router')>()),
  useNavigate: () => navigate,
}))

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    endpoints: {
      ...actual.endpoints,
      createDashboard: vi.fn(),
      addProjectTab: vi.fn(),
      removeProjectTab: vi.fn(),
      moveProjectTab: vi.fn(),
    },
  }
})

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn() }),
}))

let client: QueryClient

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

const tabs: ProjectTab[] = [
  { dashboard_id: 1, title: 'Views', owner: 'system', group_id: 1 },
  { dashboard_id: 13, title: 'Marketing', owner: 'user', group_id: 13 },
]

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.clearAllMocks()
})

describe('useProjectTabActions', () => {
  it('add sends the dashboard and where it goes, and caches the returned tabs', async () => {
    vi.mocked(endpoints.addProjectTab).mockResolvedValue({ tabs })
    const { result } = renderHook(() => useProjectTabActions(), { wrapper })

    await expect(act(() => result.current.add(7, 13, 1))).resolves.toBe(true)

    expect(endpoints.addProjectTab).toHaveBeenCalledWith(7, { dashboard_id: 13, after: 1 })
    expect(client.getQueryData(['project-tabs', 7])).toEqual({ tabs })
    expect(result.current.pending).toBe(false)
  })

  it('a refused add resolves false and toasts the server\'s message', async () => {
    vi.mocked(endpoints.addProjectTab).mockRejectedValue(new ApiError(400, 'already a tab of this project'))
    const { result } = renderHook(() => useProjectTabActions(), { wrapper })

    await expect(act(() => result.current.add(7, 13))).resolves.toBe(false)

    expect(toast.error).toHaveBeenCalledWith('already a tab of this project')
    expect(client.getQueryData(['project-tabs', 7])).toBeUndefined()
  })

  it('a dropped connection toasts "Couldn\'t reach the server" and resolves false', async () => {
    vi.mocked(endpoints.moveProjectTab).mockRejectedValue(new TypeError('Failed to fetch'))
    const { result } = renderHook(() => useProjectTabActions(), { wrapper })

    await expect(act(() => result.current.move(7, 13, 0))).resolves.toBe(false)

    expect(toast.error).toHaveBeenCalledWith("Couldn't reach the server")
  })

  it('move names the tab it goes after', async () => {
    vi.mocked(endpoints.moveProjectTab).mockResolvedValue({ tabs })
    const { result } = renderHook(() => useProjectTabActions(), { wrapper })

    await expect(act(() => result.current.move(7, 13, 0))).resolves.toBe(true)

    expect(endpoints.moveProjectTab).toHaveBeenCalledWith(7, 13, 0)
    expect(client.getQueryData(['project-tabs', 7])).toEqual({ tabs })
  })

  it('remove shows "Removed \'<title>\'" with an Undo that adds the tab back', async () => {
    vi.mocked(endpoints.removeProjectTab).mockResolvedValue({ tabs: tabs.slice(0, 1) })
    vi.mocked(endpoints.addProjectTab).mockResolvedValue({ tabs })
    const { result } = renderHook(() => useProjectTabActions(), { wrapper })

    await expect(act(() => result.current.remove(7, { dashboard_id: 13, title: 'Marketing' }))).resolves.toBe(true)

    expect(endpoints.removeProjectTab).toHaveBeenCalledWith(7, 13)
    expect(toast).toHaveBeenCalledWith("Removed 'Marketing'", { action: { label: 'Undo', onClick: expect.any(Function) } })

    const { action } = vi.mocked(toast).mock.calls[0][1] as unknown as { action: { onClick: () => void } }
    await act(async () => action.onClick())
    expect(endpoints.addProjectTab).toHaveBeenCalledWith(7, { dashboard_id: 13, after: undefined })
    expect(client.getQueryData(['project-tabs', 7])).toEqual({ tabs })
  })

  it('a fetch of the tabs still in flight when the answer comes cannot put a removed tab back', async () => {
    let answerStale!: (v: { tabs: ProjectTab[] }) => void
    // The write cancels this fetch, which rejects it: caught here, at once,
    // so the rejection is never unhandled.
    const stale = client
      .fetchQuery({
        queryKey: ['project-tabs', 7],
        queryFn: () => new Promise<{ tabs: ProjectTab[] }>((resolve) => (answerStale = resolve)),
      })
      .catch(() => undefined)
    vi.mocked(endpoints.removeProjectTab).mockResolvedValue({ tabs: tabs.slice(0, 1) })
    const { result } = renderHook(() => useProjectTabActions(), { wrapper })

    await expect(act(() => result.current.remove(7, { dashboard_id: 13, title: 'Marketing' }))).resolves.toBe(true)
    answerStale({ tabs })
    await stale

    expect(client.getQueryData(['project-tabs', 7])).toEqual({ tabs: tabs.slice(0, 1) })
  })

  it('a refused remove shows no Removed toast', async () => {
    vi.mocked(endpoints.removeProjectTab).mockRejectedValue(new ApiError(404, 'project 7 has no tab for dashboard 13'))
    const { result } = renderHook(() => useProjectTabActions(), { wrapper })

    await expect(act(() => result.current.remove(7, { dashboard_id: 13, title: 'Marketing' }))).resolves.toBe(false)

    expect(toast.error).toHaveBeenCalledWith('project 7 has no tab for dashboard 13')
    expect(toast).not.toHaveBeenCalled()
  })

  describe('create', () => {
    const created = { dashboard_id: 21, title: 'Launch' } as DashboardDetail

    it('makes the dashboard, adds it as the project\'s tab, refreshes the lists and opens it', async () => {
      vi.mocked(endpoints.createDashboard).mockResolvedValue(created)
      vi.mocked(endpoints.addProjectTab).mockResolvedValue({ tabs })
      const invalidate = vi.spyOn(client, 'invalidateQueries')
      const { result } = renderHook(() => useProjectTabActions(), { wrapper })

      await expect(act(() => result.current.create(4, 'Launch'))).resolves.toBe(true)

      expect(endpoints.createDashboard).toHaveBeenCalledWith({ title: 'Launch' })
      expect(endpoints.addProjectTab).toHaveBeenCalledWith(4, { dashboard_id: 21 })
      expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboards'] })
      expect(invalidate).toHaveBeenCalledWith({ queryKey: ['project-tabs', 4] })
      expect(navigate).toHaveBeenCalledWith(tabPath(4, 21))
      expect(result.current.pending).toBe(false)
    })

    it('a refused create toasts the message, adds no tab and opens nothing', async () => {
      vi.mocked(endpoints.createDashboard).mockRejectedValue(new ApiError(400, 'title must be at least 2 characters'))
      const { result } = renderHook(() => useProjectTabActions(), { wrapper })

      await expect(act(() => result.current.create(4, 'L'))).resolves.toBe(false)

      expect(toast.error).toHaveBeenCalledWith('title must be at least 2 characters')
      expect(endpoints.addProjectTab).not.toHaveBeenCalled()
      expect(navigate).not.toHaveBeenCalled()
    })

    it('a refused tab says the dashboard was made, and still refreshes the dashboards', async () => {
      vi.mocked(endpoints.createDashboard).mockResolvedValue(created)
      vi.mocked(endpoints.addProjectTab).mockRejectedValue(new ApiError(400, 'project is archived'))
      const invalidate = vi.spyOn(client, 'invalidateQueries')
      const { result } = renderHook(() => useProjectTabActions(), { wrapper })

      await expect(act(() => result.current.create(4, 'Launch'))).resolves.toBe(false)

      expect(toast.error).toHaveBeenCalledWith("Created 'Launch' but couldn't add it as a tab: project is archived")
      expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboards'] })
      expect(navigate).not.toHaveBeenCalled()
    })
  })
})

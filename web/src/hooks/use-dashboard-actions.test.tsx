import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { MemoryRouter, useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, endpoints, type DashboardDetail } from '@/lib/api'
import { toast } from 'sonner'
import { useDashboardActions } from './use-dashboard-actions'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    endpoints: {
      ...actual.endpoints,
      duplicate: vi.fn(),
      archive: vi.fn(),
      restore: vi.fn(),
      move: vi.fn(),
    },
  }
})

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn() }),
}))

let client: QueryClient
let location = ''

function LocationProbe() {
  location = useLocation().pathname
  return null
}

function wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/dashboards/1']}>
        <LocationProbe />
        {children}
      </MemoryRouter>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  location = ''
  vi.clearAllMocks()
})

describe('useDashboardActions', () => {
  it('archive calls endpoints.archive with wholeGroup and invalidates the lists', async () => {
    vi.mocked(endpoints.archive).mockResolvedValue({ status: 'archived' })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useDashboardActions(), { wrapper })

    await act(() => result.current.archive({ dashboard_id: 5, title: 'Marketing' }, { wholeGroup: true }))

    expect(endpoints.archive).toHaveBeenCalledWith(5, true)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboards'] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard'] })
  })

  it('shows "Archived \'<title>\'" with an Undo action', async () => {
    vi.mocked(endpoints.archive).mockResolvedValue({ status: 'archived' })
    const { result } = renderHook(() => useDashboardActions(), { wrapper })

    await act(() => result.current.archive({ dashboard_id: 5, title: 'Marketing' }, { wholeGroup: false }))

    expect(toast).toHaveBeenCalledWith("Archived 'Marketing'", {
      action: { label: 'Undo', onClick: expect.any(Function) },
    })
  })

  it('a rejected move toasts the error, resolves, and refetches', async () => {
    vi.mocked(endpoints.move).mockRejectedValue(new ApiError(409, 'conflict'))
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useDashboardActions(), { wrapper })

    await expect(act(() => result.current.move(5, { after: 3 }))).resolves.toBeUndefined()

    expect(toast.error).toHaveBeenCalledWith('conflict')
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboards'] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard'] })
  })

  it('a move rejecting with a plain TypeError (a dropped connection) toasts, resolves, and refetches', async () => {
    vi.mocked(endpoints.move).mockRejectedValue(new TypeError('Failed to fetch'))
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const { result } = renderHook(() => useDashboardActions(), { wrapper })

    await expect(act(() => result.current.move(5, { after: 3 }))).resolves.toBeUndefined()

    expect(toast.error).toHaveBeenCalledWith("Couldn't reach the server")
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboards'] })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['dashboard'] })
  })

  it('duplicate navigates to the copy', async () => {
    const copy: DashboardDetail = {
      dashboard_id: 42,
      title: 'Views (copy)',
      owner: 'user',
      group_id: 42,
      widgets: [],
      follows_project: true,
      follows_range: true,
      tabs: [],
    }
    vi.mocked(endpoints.duplicate).mockResolvedValue(copy)
    const { result } = renderHook(() => useDashboardActions(), { wrapper })

    await act(() => result.current.duplicate({ dashboard_id: 1, title: 'Views' }))

    await waitFor(() => expect(location).toBe('/dashboards/42'))
  })
})

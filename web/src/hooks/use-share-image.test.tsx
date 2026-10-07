import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { ApiError, endpoints } from '@/lib/api'
import { testClient } from '@/test/render'
import { useShareImage } from './use-share-image'

const createObjectURL = vi.fn(() => 'blob:thumb')
const revokeObjectURL = vi.fn()

beforeEach(() => {
  vi.restoreAllMocks()
  createObjectURL.mockClear()
  revokeObjectURL.mockClear()
  URL.createObjectURL = createObjectURL
  URL.revokeObjectURL = revokeObjectURL
})

function setup(id: string) {
  const client = testClient()
  return renderHook(() => useShareImage(id), {
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  })
}

describe('useShareImage', () => {
  it('fetches the share image with the console token and gives an object URL, revoked on unmount', async () => {
    const blob = new Blob(['png'], { type: 'image/png' })
    const fetchImage = vi.spyOn(endpoints, 'widgetShareImage').mockResolvedValue(blob)

    const { result, unmount } = setup('abc')
    expect(result.current.url).toBeUndefined()
    await waitFor(() => expect(result.current.url).toBe('blob:thumb'))

    expect(fetchImage).toHaveBeenCalledWith('abc')
    expect(createObjectURL).toHaveBeenCalledWith(blob)
    expect(result.current.failed).toBe(false)
    expect(revokeObjectURL).not.toHaveBeenCalled()
    unmount()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:thumb')
  })

  it('reports a failed fetch and makes no URL', async () => {
    vi.spyOn(endpoints, 'widgetShareImage').mockRejectedValue(new ApiError(404, 'not found'))

    const { result } = setup('gone')
    await waitFor(() => expect(result.current.failed).toBe(true))

    expect(result.current.url).toBeUndefined()
    expect(createObjectURL).not.toHaveBeenCalled()
  })
})

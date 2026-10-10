import { render, screen } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { endpoints } from '@/lib/api'
import { testClient } from '@/test/render'
import { AddWidgetButton, useWidgetEditing, WidgetEditingProvider, type WidgetEditing } from './WidgetEditing'

const seen: (WidgetEditing | null)[] = []

function Probe() {
  seen.push(useWidgetEditing())
  return null
}

function page(enabled: boolean, from = '2026-10-03') {
  return (
    <WidgetEditingProvider dashboardId={3} enabled={enabled} defaults={{ range: { range: '7d', from, to: '2026-10-09' }, timezone: 'UTC' }}>
      <Probe />
      <AddWidgetButton />
    </WidgetEditingProvider>
  )
}

function renderWrapped(ui: ReactNode) {
  const client = testClient()
  return render(ui, { wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> })
}

beforeEach(() => {
  seen.length = 0
  vi.restoreAllMocks()
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: [] })
  vi.spyOn(endpoints, 'schemaViews').mockResolvedValue('')
})

describe('WidgetEditingProvider', () => {
  it('hands the cards the same value across renders, whatever the defaults', () => {
    const { rerender } = renderWrapped(page(true))
    rerender(page(true, '2026-10-02'))
    expect(seen.length).toBeGreaterThan(1)
    expect(seen.every((v) => v !== null && v === seen[0])).toBe(true)
  })

  it('offers nothing while not enabled', () => {
    renderWrapped(page(false))
    expect(seen.every((v) => v === null)).toBe(true)
    expect(screen.queryByRole('button', { name: 'Add widget' })).not.toBeInTheDocument()
  })

  it('loads the editor, and what it fetches, only on the first open', async () => {
    renderWrapped(page(true))
    expect(endpoints.projects).not.toHaveBeenCalled()
    expect(endpoints.schemaViews).not.toHaveBeenCalled()

    await userEvent.click(screen.getByRole('button', { name: 'Add widget' }))
    // The first import of the editor's chunk is slow under the test runner.
    expect(await screen.findByRole('dialog', { name: 'Add widget' }, { timeout: 15_000 })).toBeInTheDocument()
    expect(endpoints.projects).toHaveBeenCalled()
  })
})

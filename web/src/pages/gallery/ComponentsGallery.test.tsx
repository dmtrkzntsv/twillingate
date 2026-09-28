import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Navigate, Route, Routes } from 'react-router'
import { widgets } from '@/components/widgets'
import { _resetForTests } from '@/lib/auth'
import { renderWithProviders } from '@/test/render'
import { addWidgetJson } from './ComponentEntry'
import ComponentsGallery from './ComponentsGallery'

// Captured before any test replaces `navigator.clipboard` (userEvent's own
// stub, or the clipboard-unavailable test below), so it can be restored.
const originalClipboard = navigator.clipboard

function renderAt(path: string) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/gallery/components" element={<ComponentsGallery />} />
        <Route path="/gallery" element={<Navigate to="/gallery/components" replace />} />
        <Route path="/gallery/*" element={<Navigate to="/gallery/components" replace />} />
      </Routes>
    </MemoryRouter>
  )
}

beforeEach(() => {
  localStorage.clear()
  _resetForTests()
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify({ dashboards: [], timezone: 'UTC' }), { status: 200 }))
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  Object.defineProperty(navigator, 'clipboard', { value: originalClipboard, configurable: true })
})

const names = Object.keys(widgets).sort()

describe(
  'components gallery',
  () => {
    it('shows a section per component, in name order, each linked from the jump list', () => {
      // getAllByRole('region') walks the accessibility tree for the whole
      // DOM, which is slow with this many components and examples; a plain
      // selector checks the same ids and order much more cheaply.
      const { container } = renderAt('/gallery/components')
      const sections = container.querySelectorAll('section[id^="component-"]')
      expect(Array.from(sections).map((s) => s.id)).toEqual(names.map((n) => `component-${n}`))
      const jump = screen.getByRole('navigation', { name: 'Components' })
      for (const name of names) {
        expect(within(jump).getByRole('link', { name })).toHaveAttribute('href', `#component-${name}`)
      }
    })

    it('shows each example in a widget card with its title', () => {
      renderAt('/gallery/components')
      const line = screen.getByRole('region', { name: 'line' })
      for (const example of widgets.line.examples) {
        expect(within(line).getByRole('heading', { name: example.title })).toBeInTheDocument()
      }
      expect(line.querySelectorAll('[data-slot="widget-card"]')).toHaveLength(widgets.line.examples.length)
    })

    it('lists the contract: columns and props', () => {
      renderAt('/gallery/components')
      const radial = screen.getByRole('region', { name: 'radial' })
      expect(within(radial).getByText(widgets.radial.contract.description)).toBeInTheDocument()
      expect(within(radial).getByText('max')).toBeInTheDocument()
      expect(within(radial).getAllByText('optional').length).toBeGreaterThan(0)
      expect(within(radial).getByText('format')).toBeInTheDocument()
    })

    it('copies the add_widget JSON of an example', async () => {
      const user = userEvent.setup()
      renderAt('/gallery/components')
      const bar = screen.getByRole('region', { name: 'bar' })
      await user.click(within(bar).getAllByRole('button', { name: /copy add_widget json/i })[0])
      expect(await navigator.clipboard.readText()).toBe(addWidgetJson('bar', widgets.bar.examples[0]))
      expect(within(bar).getAllByRole('button', { name: /copied/i })).toHaveLength(1)
    })

    it('says so when the clipboard is unavailable, and keeps the JSON readable', async () => {
      const user = userEvent.setup()
      Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
      renderAt('/gallery/components')
      const pie = screen.getByRole('region', { name: 'pie' })
      await user.click(within(pie).getAllByRole('button', { name: /copy add_widget json/i })[0])
      expect(within(pie).getByRole('button', { name: /copy failed/i })).toBeInTheDocument()
      expect(within(pie).getByText(/"component": "pie"/)).toBeInTheDocument()
    })

    it('redirects /gallery and unknown gallery paths to the components page', () => {
      for (const path of ['/gallery', '/gallery/templates']) {
        const { unmount } = renderAt(path)
        expect(screen.getByRole('heading', { level: 1, name: 'Components' })).toBeInTheDocument()
        unmount()
      }
    })
  },
  // The section-per-component test alone takes ~3s and up to 5.5s under
  // load, past vitest's 5s default; give the whole block more room.
  15000
)

describe('addWidgetJson', () => {
  it('is the component and the example props, pretty-printed', () => {
    expect(addWidgetJson('pie', { title: 't', props: { donut: true }, data: { columns: [], rows: [], truncated: false } })).toBe(
      '{\n  "component": "pie",\n  "props": {\n    "donut": true\n  }\n}'
    )
  })
})

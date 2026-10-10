import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import WidgetErrorBoundary from './WidgetErrorBoundary'

function Draw({ rows }: { rows: string[] }) {
  if (rows.length === 0) throw new TypeError('no rows')
  return <p>{rows.join(',')}</p>
}

afterEach(() => vi.restoreAllMocks())

describe('WidgetErrorBoundary', () => {
  it('shows the fallback once its children throw, and tries them again when a key changes', () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const empty: string[] = []
    const { rerender } = render(
      <WidgetErrorBoundary resetKeys={[empty]} fallback={<p>cannot draw</p>}>
        <Draw rows={empty} />
      </WidgetErrorBoundary>
    )
    expect(screen.getByText('cannot draw')).toBeInTheDocument()

    // The same keys: still the fallback.
    rerender(
      <WidgetErrorBoundary resetKeys={[empty]} fallback={<p>cannot draw</p>}>
        <Draw rows={empty} />
      </WidgetErrorBoundary>
    )
    expect(screen.getByText('cannot draw')).toBeInTheDocument()

    const rows = ['a', 'b']
    rerender(
      <WidgetErrorBoundary resetKeys={[rows]} fallback={<p>cannot draw</p>}>
        <Draw rows={rows} />
      </WidgetErrorBoundary>
    )
    expect(screen.getByText('a,b')).toBeInTheDocument()
  })
})

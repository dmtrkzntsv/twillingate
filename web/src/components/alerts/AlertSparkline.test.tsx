import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import type { Alert } from '@/lib/api'
import AlertSparkline from './AlertSparkline'

type Recent = Alert['recent']

describe('AlertSparkline', () => {
  it('draws nothing with fewer than two values', () => {
    const recent: Recent = [
      { at: '2026-11-26T06:00:00Z', value: 4, outcome: 'ok' },
      { at: '2026-11-25T06:00:00Z', value: null, outcome: 'no_data' },
    ]
    const { container } = render(<AlertSparkline recent={recent} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('draws the values, hidden from screen readers, with a red dot on each breached one', () => {
    const recent: Recent = [
      { at: '2026-11-26T06:00:00Z', value: 2, outcome: 'breached' },
      { at: '2026-11-25T06:00:00Z', value: 9, outcome: 'ok' },
      { at: '2026-11-24T06:00:00Z', value: 3, outcome: 'breached' },
    ]
    const before = JSON.stringify(recent)
    const { container } = render(<AlertSparkline recent={recent} />)
    const chart = container.firstElementChild!
    expect(chart).toHaveAttribute('aria-hidden', 'true')
    expect(chart.querySelectorAll('circle[data-breached]')).toHaveLength(2)
    // The list's cache is not reordered in place.
    expect(JSON.stringify(recent)).toBe(before)
  })
})

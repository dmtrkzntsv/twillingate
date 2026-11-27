import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import type { AlertState } from '@/lib/api'
import { alert } from '@/test/alerts'
import AlertStateBadge from './AlertStateBadge'

describe('AlertStateBadge', () => {
  it.each<[AlertState, string]>([
    ['pending', 'Pending'],
    ['ok', 'OK'],
    ['breached', 'Breached'],
    ['no_data', 'No data'],
    ['broken', 'Broken'],
    ['paused', 'Paused'],
  ])('says %s as %s', (state, text) => {
    render(<AlertStateBadge alert={alert({ state })} />)
    expect(screen.getByText(text)).toBeInTheDocument()
  })

  it('says since when the current breach began, from the recent evaluations', () => {
    const recent = [
      { at: '2026-11-26T12:00:00Z', value: 10, outcome: 'breached' as const },
      { at: '2026-11-25T12:00:00Z', value: null, outcome: 'no_data' as const },
      { at: '2026-11-24T12:00:00Z', value: 12, outcome: 'breached' as const },
      { at: '2026-11-23T12:00:00Z', value: 80, outcome: 'ok' as const },
      { at: '2026-11-20T12:00:00Z', value: 9, outcome: 'breached' as const },
    ]
    render(<AlertStateBadge alert={alert({ state: 'breached', recent })} />)
    expect(screen.getByText('Breached since Nov 24')).toBeInTheDocument()
  })

  it('says until when a snooze ends, in local time', () => {
    const until = new Date(2026, 10, 27, 9, 5).toISOString()
    render(<AlertStateBadge alert={alert({ state: 'snoozed', snoozed_until: until })} />)
    expect(screen.getByText('Snoozed until Nov 27, 09:05')).toBeInTheDocument()
  })

  it('colours breached red, ok green and broken amber, as the Inbox does', () => {
    const { rerender } = render(<AlertStateBadge alert={alert({ state: 'breached' })} />)
    expect(screen.getByText('Breached').closest('[data-slot=badge]')).toHaveClass('text-destructive')
    rerender(<AlertStateBadge alert={alert({ state: 'ok' })} />)
    expect(screen.getByText('OK').closest('[data-slot=badge]')).toHaveClass('text-emerald-600')
    rerender(<AlertStateBadge alert={alert({ state: 'broken' })} />)
    expect(screen.getByText('Broken').closest('[data-slot=badge]')).toHaveClass('text-amber-600')
  })
})

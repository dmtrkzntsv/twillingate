import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { formatDayTime } from '@/lib/forms'
import { fromNow } from '@/test/forms'
import StopDialog from './StopDialog'

const RFC3339 = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/

describe('StopDialog', () => {
  it('stops the form from this second, and closes once saved', async () => {
    const user = userEvent.setup()
    const onStop = vi.fn().mockResolvedValue(true)
    const onOpenChange = vi.fn()
    render(<StopDialog open onOpenChange={onOpenChange} name="contact" onStop={onStop} />)
    expect(screen.getByRole('dialog', { name: 'Stop accepting contact' })).toBeInTheDocument()
    const before = Math.floor(Date.now() / 1000) * 1000
    await user.click(screen.getByRole('button', { name: 'Stop now' }))
    const sent = onStop.mock.calls[0][0] as string
    expect(sent).toMatch(RFC3339)
    expect(Date.parse(sent)).toBeGreaterThanOrEqual(before)
    expect(Date.parse(sent)).toBeLessThanOrEqual(Date.now())
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it('sends a picked local date and time as UTC', async () => {
    const user = userEvent.setup()
    const onStop = vi.fn().mockResolvedValue(true)
    render(<StopDialog open onOpenChange={vi.fn()} name="contact" onStop={onStop} />)
    expect(screen.getByRole('button', { name: 'Stop at this time' })).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Stop at'), { target: { value: '2030-10-09T17:00' } })
    await user.click(screen.getByRole('button', { name: 'Stop at this time' }))
    expect(onStop).toHaveBeenCalledWith(new Date('2030-10-09T17:00').toISOString().replace(/\.\d{3}Z$/, 'Z'))
  })

  it('says when a form already set to stop does, and stays open on a refusal', async () => {
    const user = userEvent.setup()
    const at = fromNow(2)
    const onOpenChange = vi.fn()
    render(<StopDialog open onOpenChange={onOpenChange} name="contact" closesAt={at} onStop={vi.fn().mockResolvedValue(false)} />)
    expect(screen.getByText(new RegExp(`It stops ${formatDayTime(new Date(at))}`))).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Stop now' }))
    expect(onOpenChange).not.toHaveBeenCalled()
  })

  it('waits while a write runs', () => {
    render(<StopDialog open onOpenChange={vi.fn()} name="contact" pending onStop={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Stop now' })).toBeDisabled()
  })
})

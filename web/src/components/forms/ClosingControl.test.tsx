import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { fromNow } from '@/test/forms'
import ClosingControl from './ClosingControl'

const RFC3339 = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/

describe('ClosingControl', () => {
  it('reads Open, and Stop now closes the form from this second', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn().mockResolvedValue(true)
    render(<ClosingControl onChange={onChange} />)
    expect(screen.getByText('Open')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reopen' })).toBeNull()
    const before = Math.floor(Date.now() / 1000) * 1000
    await user.click(screen.getByRole('button', { name: 'Stop now' }))
    const sent = onChange.mock.calls[0][0] as string
    expect(sent).toMatch(RFC3339)
    expect(Date.parse(sent)).toBeGreaterThanOrEqual(before)
    expect(Date.parse(sent)).toBeLessThanOrEqual(Date.now())
  })

  it('sends a picked local date and time as UTC', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn().mockResolvedValue(true)
    render(<ClosingControl onChange={onChange} />)
    expect(screen.getByRole('button', { name: 'Set' })).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Closes at'), { target: { value: '2030-10-09T17:00' } })
    await user.click(screen.getByRole('button', { name: 'Set' }))
    expect(onChange).toHaveBeenCalledWith(new Date('2030-10-09T17:00').toISOString().replace(/\.\d{3}Z$/, 'Z'))
  })

  it('reopens a closed form, and a form closing later', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn().mockResolvedValue(true)
    const { rerender } = render(<ClosingControl closesAt={fromNow(-1)} onChange={onChange} />)
    expect(screen.getByText(/^Closed/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Stop now' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Reopen' }))
    expect(onChange).toHaveBeenLastCalledWith(null)

    rerender(<ClosingControl closesAt={fromNow(2)} onChange={onChange} />)
    expect(screen.getByText(/^Closes [A-Z]/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Stop now' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Reopen' }))
    expect(onChange).toHaveBeenLastCalledWith(null)
  })

  it('waits while a write runs', () => {
    render(<ClosingControl closesAt={fromNow(2)} pending onChange={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Stop now' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Reopen' })).toBeDisabled()
  })
})

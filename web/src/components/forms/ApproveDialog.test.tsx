import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ApproveDialog, { FieldPicker } from './ApproveDialog'

describe('ApproveDialog', () => {
  it('preselects every field and approves with those still checked', async () => {
    const user = userEvent.setup()
    const onApprove = vi.fn().mockResolvedValue(true)
    const onOpenChange = vi.fn()
    render(<ApproveDialog open name="contact" fields={['email', 'message', 'phone']} onOpenChange={onOpenChange} onApprove={onApprove} />)
    const dialog = screen.getByRole('dialog', { name: 'Approve contact' })
    for (const f of ['email', 'message', 'phone']) expect(within(dialog).getByRole('checkbox', { name: f })).toBeChecked()
    await user.click(within(dialog).getByRole('checkbox', { name: 'phone' }))
    await user.click(within(dialog).getByRole('button', { name: 'Approve' }))
    expect(onApprove).toHaveBeenCalledWith(['email', 'message'])
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it('needs at least one field', async () => {
    const user = userEvent.setup()
    const onApprove = vi.fn()
    render(<ApproveDialog open name="contact" fields={['email', 'message']} onOpenChange={() => {}} onApprove={onApprove} />)
    await user.click(screen.getByRole('checkbox', { name: 'email' }))
    await user.click(screen.getByRole('checkbox', { name: 'message' }))
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled()
    await user.click(screen.getByRole('checkbox', { name: 'message' }))
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled()
  })

  it('stays open when the approval is refused', async () => {
    const user = userEvent.setup()
    const onOpenChange = vi.fn()
    render(<ApproveDialog open name="contact" fields={['email']} onOpenChange={onOpenChange} onApprove={vi.fn().mockResolvedValue(false)} />)
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
  })
})

describe('FieldPicker', () => {
  it('lists the kept fields first, in their order, and marks one arriving outside them as not kept', () => {
    render(<FieldPicker fields={['email', 'message', 'phone']} kept={['message', 'email']} checked={['message', 'email']} onChange={() => {}} />)
    const names = screen.getAllByRole('checkbox').map((b) => document.querySelector(`label[for="${b.id}"]`)?.textContent)
    expect(names).toEqual(['message', 'email', 'phone'])
    expect(screen.getByRole('checkbox', { name: 'phone' })).not.toBeChecked()
    expect(screen.getAllByText('not kept')).toHaveLength(1)
  })

  it('keeps the order when a field is unchecked and appends one checked again', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<FieldPicker fields={['email', 'message', 'phone']} kept={['message', 'email']} checked={['message', 'email']} onChange={onChange} />)
    await user.click(screen.getByRole('checkbox', { name: 'phone' }))
    expect(onChange).toHaveBeenLastCalledWith(['message', 'email', 'phone'])
    await user.click(screen.getByRole('checkbox', { name: 'message' }))
    expect(onChange).toHaveBeenLastCalledWith(['email'])
  })
})

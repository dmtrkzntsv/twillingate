import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { draft, form } from '@/test/forms'
import SettingsDialog from './SettingsDialog'

describe('SettingsDialog', () => {
  it('sends only what was edited here, never a change made elsewhere while it was open', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn().mockResolvedValue(true)
    const opened = form('contact', { purpose: 'Sales', return_url: '', expected_fields: ['email'], fields: ['email'] })
    const { rerender } = render(<SettingsDialog open onOpenChange={vi.fn()} form={opened} onSave={onSave} />)
    // Another admin sets a return URL; a refetch brings it in while the dialog is open.
    rerender(<SettingsDialog open onOpenChange={vi.fn()} form={{ ...opened, return_url: 'https://elsewhere.example/thanks' }} onSave={onSave} />)
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    await user.clear(screen.getByLabelText('Purpose'))
    await user.type(screen.getByLabelText('Purpose'), 'Leads')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledWith({ purpose: 'Leads' }))
  })
})

describe('SettingsDialog submission notifications', () => {
  it('turns notifications off on an approved form, sending only that', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn().mockResolvedValue(true)
    render(<SettingsDialog open onOpenChange={vi.fn()} form={form('contact', { notify_submissions: true })} onSave={onSave} />)
    const toggle = screen.getByRole('switch', { name: 'Notify on each submission' })
    expect(toggle).toBeChecked()
    await user.click(toggle)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledWith({ notify_submissions: false }))
  })

  it('offers no switch on a draft, which approval turns it on for', () => {
    render(<SettingsDialog open onOpenChange={vi.fn()} form={draft('contact', 5)} onSave={vi.fn()} />)
    expect(screen.queryByRole('switch', { name: 'Notify on each submission' })).not.toBeInTheDocument()
  })
})

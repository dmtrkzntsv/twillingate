import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import GroupNameField from './GroupNameField'

function setup(onRename = vi.fn().mockResolvedValue(true), props: { named?: boolean; pending?: boolean } = {}) {
  const onDone = vi.fn()
  renderWithProviders(
    <GroupNameField name="Marketing" named={props.named ?? true} pending={props.pending} onRename={onRename} onDone={onDone} />
  )
  return { onRename, onDone, field: screen.getByRole('textbox', { name: 'Group name' }) }
}

describe('GroupNameField', () => {
  it('shows the name in a focused field', () => {
    const { field } = setup()
    expect(field).toHaveValue('Marketing')
    expect(field).toHaveFocus()
  })

  it('renames on Enter, then closes once the save resolves true', async () => {
    const user = userEvent.setup()
    const { onRename, onDone, field } = setup()
    await user.clear(field)
    await user.type(field, 'Ops{Enter}')
    expect(onRename).toHaveBeenCalledWith('Ops')
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1))
  })

  it('closes without a save on Escape and on the cancel button', async () => {
    const user = userEvent.setup()
    const { onRename, onDone, field } = setup()
    await user.type(field, 'x{Escape}')
    expect(onDone).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('button', { name: 'Cancel rename' }))
    expect(onDone).toHaveBeenCalledTimes(2)
    expect(onRename).not.toHaveBeenCalled()
  })

  it('closes without a save when a named group is unchanged', async () => {
    const user = userEvent.setup()
    const { onRename, onDone } = setup()
    await user.keyboard('{Enter}')
    expect(onDone).toHaveBeenCalledTimes(1)
    expect(onRename).not.toHaveBeenCalled()
  })

  it('sends the unchanged fallback for an unnamed group, which names it', async () => {
    const user = userEvent.setup()
    const { onRename, onDone } = setup(undefined, { named: false })
    await user.keyboard('{Enter}')
    expect(onRename).toHaveBeenCalledWith('Marketing')
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1))
  })

  it('sends nothing while a save is pending, so a second Enter cannot double-send', () => {
    const { onRename, field } = setup(undefined, { pending: true })
    fireEvent.change(field, { target: { value: 'Ops' } })
    fireEvent.submit(field.closest('form')!)
    expect(onRename).not.toHaveBeenCalled()
  })

  it.each(['', ' ', 'a'])('never sends %j: invalid, save disabled, still open', async (text) => {
    const user = userEvent.setup()
    const { onRename, onDone, field } = setup()
    await user.clear(field)
    if (text) await user.type(field, text)
    await user.keyboard('{Enter}')
    expect(onRename).not.toHaveBeenCalled()
    expect(onDone).not.toHaveBeenCalled()
    expect(field).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('button', { name: 'Save group name' })).toBeDisabled()
    expect(field).toBeInTheDocument()
  })

  it('stays open when the save is refused', async () => {
    const user = userEvent.setup()
    const { onRename, onDone, field } = setup(vi.fn().mockResolvedValue(false))
    await user.clear(field)
    await user.type(field, 'Ops{Enter}')
    expect(onRename).toHaveBeenCalledWith('Ops')
    expect(onDone).not.toHaveBeenCalled()
    expect(field).toHaveValue('Ops')
  })
})

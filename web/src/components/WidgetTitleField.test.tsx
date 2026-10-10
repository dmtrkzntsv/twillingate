import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import WidgetTitleField from './WidgetTitleField'

function setup(onSave = vi.fn().mockResolvedValue(true)) {
  const onDone = vi.fn()
  render(
    <>
      <WidgetTitleField title="Visits" onSave={onSave} onDone={onDone} />
      <button type="button">elsewhere</button>
    </>
  )
  return { onSave, onDone, field: screen.getByRole('textbox', { name: 'Widget title' }) }
}

describe('WidgetTitleField', () => {
  it('opens on the title, focused', () => {
    const { field } = setup()
    expect(field).toHaveValue('Visits')
    expect(field).toHaveFocus()
  })

  it('saves the trimmed title on Enter, then closes', async () => {
    const { onSave, onDone, field } = setup()
    await userEvent.clear(field)
    await userEvent.type(field, '  Daily visits  {Enter}')
    expect(onSave).toHaveBeenCalledWith('Daily visits')
    expect(onDone).toHaveBeenCalled()
  })

  it('saves with ✓', async () => {
    const { onSave, onDone, field } = setup()
    await userEvent.clear(field)
    await userEvent.type(field, 'Daily visits')
    await userEvent.click(screen.getByRole('button', { name: 'Save title' }))
    expect(onSave).toHaveBeenCalledWith('Daily visits')
    expect(onDone).toHaveBeenCalled()
  })

  it('cancels on Escape without a save', async () => {
    const { onSave, onDone, field } = setup()
    await userEvent.type(field, ' changed{Escape}')
    expect(onSave).not.toHaveBeenCalled()
    expect(onDone).toHaveBeenCalled()
  })

  it('cancels with ×', async () => {
    const { onSave, onDone } = setup()
    await userEvent.click(screen.getByRole('button', { name: 'Cancel rename' }))
    expect(onSave).not.toHaveBeenCalled()
    expect(onDone).toHaveBeenCalled()
  })

  it('closes on blur when the title is unchanged', async () => {
    const { onSave, onDone } = setup()
    await userEvent.click(screen.getByRole('button', { name: 'elsewhere' }))
    expect(onSave).not.toHaveBeenCalled()
    expect(onDone).toHaveBeenCalled()
  })

  it('stays open on blur with a change', async () => {
    const { onSave, onDone, field } = setup()
    await userEvent.type(field, ' changed')
    await userEvent.click(screen.getByRole('button', { name: 'elsewhere' }))
    expect(onSave).not.toHaveBeenCalled()
    expect(onDone).not.toHaveBeenCalled()
  })

  it.each([
    ['empty', '{Enter}'],
    ['whitespace-only', '   {Enter}'],
  ])('cancels an %s title without a save', async (_, keys) => {
    const { onSave, onDone, field } = setup()
    await userEvent.clear(field)
    await userEvent.type(field, keys)
    expect(onSave).not.toHaveBeenCalled()
    expect(onDone).toHaveBeenCalled()
  })

  it('cancels an unchanged title on Enter without a save', async () => {
    const { onSave, onDone, field } = setup()
    await userEvent.type(field, '{Enter}')
    expect(onSave).not.toHaveBeenCalled()
    expect(onDone).toHaveBeenCalled()
  })

  it('stays open, keeping the input, when the save is refused', async () => {
    const { onSave, onDone, field } = setup(vi.fn().mockResolvedValue(false))
    await userEvent.clear(field)
    await userEvent.type(field, 'Daily visits{Enter}')
    expect(onSave).toHaveBeenCalledWith('Daily visits')
    expect(onDone).not.toHaveBeenCalled()
    expect(field).toHaveValue('Daily visits')
  })
})

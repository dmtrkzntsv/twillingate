import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import Splitter from './Splitter'

function setup(value = 50) {
  const onChange = vi.fn()
  const onReset = vi.fn()
  render(
    <Splitter
      orientation="vertical"
      label="Resize the halves"
      value={value}
      min={25}
      max={75}
      step={2}
      perPx={0.1}
      onChange={onChange}
      onReset={onReset}
      style={{}}
    />
  )
  return { onChange, onReset, handle: screen.getByRole('separator', { name: 'Resize the halves' }) }
}

describe('Splitter', () => {
  it('moves by a step per arrow key and goes to the ends with Home and End', async () => {
    const { onChange, handle } = setup()
    handle.focus()
    await userEvent.keyboard('{ArrowRight}')
    expect(onChange).toHaveBeenLastCalledWith(52)
    await userEvent.keyboard('{ArrowLeft}')
    expect(onChange).toHaveBeenLastCalledWith(48)
    await userEvent.keyboard('{End}')
    expect(onChange).toHaveBeenLastCalledWith(75)
    await userEvent.keyboard('{Home}')
    expect(onChange).toHaveBeenLastCalledWith(25)
  })

  it('follows a drag, held within its bounds, and resets on a double click', () => {
    const { onChange, onReset, handle } = setup()
    fireEvent.pointerDown(handle, { button: 0, clientX: 500, pointerId: 1 })
    fireEvent.pointerMove(handle, { clientX: 600, pointerId: 1 })
    expect(onChange).toHaveBeenLastCalledWith(60)
    fireEvent.pointerMove(handle, { clientX: 1000, pointerId: 1 })
    expect(onChange).toHaveBeenLastCalledWith(75)
    fireEvent.pointerUp(handle, { pointerId: 1 })
    onChange.mockClear()
    fireEvent.pointerMove(handle, { clientX: 700, pointerId: 1 })
    expect(onChange).not.toHaveBeenCalled()
    fireEvent.doubleClick(handle)
    expect(onReset).toHaveBeenCalled()
  })

  it('says where it is', () => {
    const { handle } = setup(40)
    expect(handle).toHaveAttribute('aria-orientation', 'vertical')
    expect(handle).toHaveAttribute('aria-valuenow', '40')
  })
})

import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ChipsInput from './ChipsInput'

describe('ChipsInput', () => {
  it('adds a trimmed value on Enter or comma, never twice, and removes one', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    const { rerender } = render(<ChipsInput label="Origins" value={['https://a.example']} onChange={onChange} />)
    await user.type(screen.getByLabelText('Origins'), '  https://b.example {Enter}')
    expect(onChange).toHaveBeenLastCalledWith(['https://a.example', 'https://b.example'])
    await user.type(screen.getByLabelText('Origins'), 'https://a.example,')
    expect(onChange).toHaveBeenCalledTimes(1)
    rerender(<ChipsInput label="Origins" value={['https://a.example', 'https://b.example']} onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: 'Remove https://a.example' }))
    expect(onChange).toHaveBeenLastCalledWith(['https://b.example'])
  })

  it('turns typed or pasted comma-separated text into one chip per piece, skipping empties and duplicates', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<ChipsInput label="Origins" value={['https://a.com']} onChange={onChange} />)
    await user.click(screen.getByLabelText('Origins'))
    await user.paste('https://a.com, https://b.com,, https://c.com ,')
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenLastCalledWith(['https://a.com', 'https://b.com', 'https://c.com'])
  })

  it('adds the pieces of text typed with a comma in the middle', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<ChipsInput label="Origins" value={[]} onChange={onChange} />)
    await user.type(screen.getByLabelText('Origins'), 'https://a.com, https://b.com{Enter}')
    expect(onChange).toHaveBeenNthCalledWith(1, ['https://a.com'])
    expect(onChange).toHaveBeenNthCalledWith(2, ['https://b.com'])
  })
})

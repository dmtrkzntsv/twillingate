import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import OriginsField from './OriginsField'

describe('OriginsField', () => {
  it('edits origins as one row each, adds and removes rows', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<OriginsField value={['https://a.example', '*']} onChange={onChange} />)
    expect(screen.getAllByRole('textbox', { name: /Origin \d/ })).toHaveLength(2)
    await user.click(screen.getByRole('button', { name: 'Remove origin 1' }))
    expect(onChange).toHaveBeenLastCalledWith(['*'])
    await user.click(screen.getByRole('button', { name: 'Add origin' }))
    expect(onChange).toHaveBeenLastCalledWith(['https://a.example', '*', ''])
    expect(screen.getByText(/With none, browsers can't send/)).toBeInTheDocument()
  })
})

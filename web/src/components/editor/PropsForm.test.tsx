import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import PropsForm from './PropsForm'

describe('PropsForm', () => {
  it('sets an enum and clears it back to the default', async () => {
    const onChange = vi.fn()
    render(<PropsForm component="line" value={{ curve: 'step' }} columns={[]} onChange={onChange} onValidity={() => {}} />)
    await userEvent.selectOptions(screen.getByLabelText('curve'), 'linear')
    expect(onChange).toHaveBeenLastCalledWith({ curve: 'linear' })
    await userEvent.selectOptions(screen.getByLabelText('curve'), '')
    expect(onChange).toHaveBeenLastCalledWith({})
  })
  it('toggles a boolean', async () => {
    const onChange = vi.fn()
    render(<PropsForm component="pie" value={{}} columns={[]} onChange={onChange} onValidity={() => {}} />)
    await userEvent.click(screen.getByRole('switch', { name: 'donut' }))
    expect(onChange).toHaveBeenLastCalledWith({ donut: true })
  })
  it('edits a column → format map with the run\'s columns offered', async () => {
    const onChange = vi.fn()
    render(<PropsForm component="table" value={{}} columns={['page', 'views']} onChange={onChange} onValidity={() => {}} />)
    await userEvent.selectOptions(screen.getByLabelText('formats: views'), 'percent')
    expect(onChange).toHaveBeenLastCalledWith({ formats: { views: 'percent' } })
  })
  it('marks bad json and reports it invalid', async () => {
    const onValidity = vi.fn()
    render(<PropsForm component="table" value={{}} columns={[]} onChange={() => {}} onValidity={onValidity} />)
    await userEvent.type(screen.getByLabelText('colorscale'), '[[oops')
    expect(screen.getByText(/not valid JSON/)).toBeInTheDocument()
    expect(onValidity).toHaveBeenLastCalledWith(false)
  })
  it('edits everything as one JSON object', async () => {
    const onChange = vi.fn()
    render(<PropsForm component="line" value={{ curve: 'step' }} columns={[]} onChange={onChange} onValidity={() => {}} />)
    await userEvent.click(screen.getByRole('button', { name: 'Edit props as JSON' }))
    const box = screen.getByLabelText('Props JSON')
    await userEvent.clear(box)
    await userEvent.type(box, '{{"format":"percent"}')
    expect(onChange).toHaveBeenLastCalledWith({ format: 'percent' })
  })
  it('drops a cleared format and the map when it empties, keeping key order', async () => {
    const onChange = vi.fn()
    render(<PropsForm component="table" value={{ mode: 'remote', formats: { views: 'percent' } }} columns={['views']} onChange={onChange} onValidity={() => {}} />)
    await userEvent.selectOptions(screen.getByLabelText('formats: views'), '')
    expect(onChange).toHaveBeenLastCalledWith({ mode: 'remote' })
  })
  it('reports valid again once the json parses, and says so for no props', async () => {
    const onValidity = vi.fn()
    const { unmount } = render(<PropsForm component="table" value={{}} columns={[]} onChange={() => {}} onValidity={onValidity} />)
    const box = screen.getByLabelText('colorscale')
    await userEvent.type(box, '[[')
    expect(onValidity).toHaveBeenLastCalledWith(false)
    await userEvent.type(box, ']')
    expect(onValidity).toHaveBeenLastCalledWith(true)
    unmount()
    render(<PropsForm component="markdown" value={{}} columns={[]} onChange={() => {}} onValidity={() => {}} />)
    expect(screen.getByText('This visualization has none')).toBeInTheDocument()
  })
  it('reports valid when it unmounts with bad json', async () => {
    const onValidity = vi.fn()
    const { unmount } = render(<PropsForm component="table" value={{}} columns={[]} onChange={() => {}} onValidity={onValidity} />)
    await userEvent.type(screen.getByLabelText('colorscale'), '[[oops')
    expect(onValidity).toHaveBeenLastCalledWith(false)
    unmount()
    expect(onValidity).toHaveBeenLastCalledWith(true)
  })
  it('drops stale json text and validity when the component changes in place', async () => {
    const onValidity = vi.fn()
    const props = { value: {}, columns: [], onChange: () => {}, onValidity }
    const { rerender } = render(<PropsForm component="table" {...props} />)
    await userEvent.type(screen.getByLabelText('colorscale'), '[[oops')
    rerender(<PropsForm component="line" {...props} />)
    expect(screen.queryByLabelText('colorscale')).toBeNull()
    expect(onValidity).toHaveBeenLastCalledWith(true)
    rerender(<PropsForm component="table" {...props} />)
    expect(screen.getByLabelText('colorscale')).toHaveValue('')
  })
})

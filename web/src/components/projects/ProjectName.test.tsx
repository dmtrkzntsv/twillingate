import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import ProjectName from './ProjectName'

describe('ProjectName', () => {
  it('heads the page with the name and a pencil', () => {
    renderWithProviders(<ProjectName name="dev" onRename={vi.fn()} />)
    expect(screen.getByRole('heading', { level: 1, name: 'dev' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Rename' })).toBeInTheDocument()
  })

  it('renames in place on Enter, then shows the heading again', async () => {
    const user = userEvent.setup()
    const onRename = vi.fn().mockResolvedValue(true)
    renderWithProviders(<ProjectName name="dev" onRename={onRename} />)
    await user.click(screen.getByRole('button', { name: 'Rename' }))
    const field = screen.getByRole('textbox', { name: 'Project name' })
    expect(field).toHaveFocus()
    await user.clear(field)
    await user.type(field, '  prod {Enter}')
    expect(onRename).toHaveBeenCalledWith('prod')
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('keeps the field open when the save is refused', async () => {
    const user = userEvent.setup()
    const onRename = vi.fn().mockResolvedValue(false)
    renderWithProviders(<ProjectName name="dev" onRename={onRename} />)
    await user.click(screen.getByRole('button', { name: 'Rename' }))
    await user.type(screen.getByRole('textbox', { name: 'Project name' }), '2')
    await user.click(screen.getByRole('button', { name: 'Save name' }))
    expect(onRename).toHaveBeenCalledWith('dev2')
    expect(screen.getByRole('textbox', { name: 'Project name' })).toHaveValue('dev2')
  })

  it('leaves the name as it was on Escape, and sends nothing unchanged or blank', async () => {
    const user = userEvent.setup()
    const onRename = vi.fn()
    renderWithProviders(<ProjectName name="dev" onRename={onRename} />)
    await user.click(screen.getByRole('button', { name: 'Rename' }))
    await user.type(screen.getByRole('textbox', { name: 'Project name' }), 'x{Escape}')
    expect(screen.getByRole('heading', { level: 1, name: 'dev' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Rename' }))
    await user.keyboard('{Enter}')
    await user.click(screen.getByRole('button', { name: 'Rename' }))
    await user.clear(screen.getByRole('textbox', { name: 'Project name' }))
    await user.keyboard('{Enter}')
    expect(onRename).not.toHaveBeenCalled()
    expect(screen.getByRole('heading', { level: 1, name: 'dev' })).toBeInTheDocument()
  })
})

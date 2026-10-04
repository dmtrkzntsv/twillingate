import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { endpoints } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import ProjectFormDialog from './ProjectFormDialog'

describe('ProjectFormDialog', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'receivedAttributes')
  })

  it('trims origins and drops empty rows on save, sending the name and origins only', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockResolvedValue(true)
    renderWithProviders(
      <ProjectFormDialog open onOpenChange={vi.fn()} title="Edit" submitLabel="Save" onSubmit={onSubmit}
        initial={{ name: 'dev', allowed_origins: [' https://a.example '] }} />,
    )
    await user.click(screen.getByRole('button', { name: 'Add origin' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSubmit).toHaveBeenCalledWith({ name: 'dev', allowed_origins: ['https://a.example'] })
  })

  it('creates with a name and no origins, and no attributes', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockResolvedValue(true)
    const onOpenChange = vi.fn()
    renderWithProviders(<ProjectFormDialog open onOpenChange={onOpenChange} title="New project" submitLabel="Create" onSubmit={onSubmit} />)
    await user.type(screen.getByLabelText('Name'), ' shop ')
    await user.click(screen.getByRole('button', { name: 'Create' }))
    expect(onSubmit).toHaveBeenCalledWith({ name: 'shop', allowed_origins: [] })
    expect(onSubmit.mock.calls[0][0]).not.toHaveProperty('attributes')
    await vi.waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it('has no breakdowns to pick, and asks for no received attributes', () => {
    renderWithProviders(
      <ProjectFormDialog open onOpenChange={vi.fn()} title="Edit" submitLabel="Save" onSubmit={vi.fn()} initial={{ name: 'dev', allowed_origins: [] }} />,
    )
    expect(screen.queryByText('Breakdowns')).not.toBeInTheDocument()
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Key not received yet' })).not.toBeInTheDocument()
    expect(endpoints.receivedAttributes).not.toHaveBeenCalled()
  })

  it('keeps Save disabled without a name, and while pending', () => {
    const { unmount } = renderWithProviders(<ProjectFormDialog open onOpenChange={vi.fn()} title="New" submitLabel="Create" onSubmit={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Create' })).toBeDisabled()
    unmount()
    renderWithProviders(<ProjectFormDialog open onOpenChange={vi.fn()} title="Edit" submitLabel="Save" pending onSubmit={vi.fn()} initial={{ name: 'dev' }} />)
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('stays open when the save is refused', async () => {
    const user = userEvent.setup()
    const onOpenChange = vi.fn()
    renderWithProviders(
      <ProjectFormDialog open onOpenChange={onOpenChange} title="Edit" submitLabel="Save" onSubmit={vi.fn().mockResolvedValue(false)} initial={{ name: 'dev', allowed_origins: [] }} />,
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onOpenChange).not.toHaveBeenCalled()
  })
})

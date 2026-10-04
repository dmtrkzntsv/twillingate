import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { endpoints } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import ProjectFormDialog from './ProjectFormDialog'

const answer = {
  project_id: 1, from: '2026-09-05', to: '2026-10-04', values_cap: 50, breakdowns_used: 3, breakdowns_max: 3,
  keys: [{ key: 'order_id', events: 980, max_values: 412, declared: false }],
}

describe('ProjectFormDialog', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue(answer)
  })

  it('trims origins and drops empty rows on save', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockResolvedValue(true)
    renderWithProviders(
      <ProjectFormDialog open onOpenChange={vi.fn()} title="Edit" projectId={1} submitLabel="Save" onSubmit={onSubmit}
        initial={{ name: 'dev', allowed_origins: [' https://a.example '], attributes: [] }} />,
    )
    await user.click(screen.getByRole('button', { name: 'Add origin' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSubmit).toHaveBeenCalledWith({ name: 'dev', allowed_origins: ['https://a.example'], attributes: [] })
  })

  it('disables Save with the reason while the breakdowns are over the limit', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <ProjectFormDialog open onOpenChange={vi.fn()} title="Edit" projectId={1} submitLabel="Save" onSubmit={vi.fn()}
        initial={{ name: 'dev', allowed_origins: [], attributes: [] }} />,
    )
    const save = screen.getByRole('button', { name: 'Save' })
    await user.click(await screen.findByRole('checkbox', { name: /order_id/ }))
    expect(save).toBeDisabled()
    expect(save).toHaveAttribute('title', expect.stringMatching(/over the limit/))
    await user.click(screen.getByRole('checkbox', { name: /order_id/ }))
    expect(save).toBeEnabled()
  })

  it('saves a key typed in the breakdown field without Enter', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'receivedAttributes').mockResolvedValue({ ...answer, breakdowns_used: 0 })
    const onSubmit = vi.fn().mockResolvedValue(true)
    renderWithProviders(
      <ProjectFormDialog open onOpenChange={vi.fn()} title="Edit" projectId={1} submitLabel="Save" onSubmit={onSubmit}
        initial={{ name: 'dev', allowed_origins: [], attributes: [] }} />,
    )
    await user.type(await screen.findByRole('textbox', { name: 'Key not received yet' }), 'tier')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSubmit).toHaveBeenCalledWith({ name: 'dev', allowed_origins: [], attributes: ['tier'] })
  })
})

import { describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Project } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import OriginsSection from './OriginsSection'

const project = { project_id: 1, name: 'dev', allowed_origins: ['https://a.example', '*'], attributes: ['plan'] } as Project

describe('OriginsSection', () => {
  it('lists origins one per line, and nothing else', () => {
    renderWithProviders(<OriginsSection project={project} onSave={vi.fn()} />)
    const origins = screen.getByRole('list', { name: 'Allowed origins' })
    expect(within(origins).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['https://a.example', '*'])
    expect(screen.queryByText('Breakdowns')).not.toBeInTheDocument()
    expect(screen.queryByText('Details')).not.toBeInTheDocument()
  })

  it('says when browsers cannot send', () => {
    renderWithProviders(<OriginsSection project={{ ...project, allowed_origins: [] }} onSave={vi.fn()} />)
    expect(screen.getByText('None: browsers cannot send')).toBeInTheDocument()
  })

  it('edits the origins alone, without the name', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn().mockResolvedValue(true)
    renderWithProviders(<OriginsSection project={project} onSave={onSave} />)
    await user.click(screen.getByRole('button', { name: 'Edit' }))
    expect(screen.queryByLabelText('Name')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Remove origin 2' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledWith({ allowed_origins: ['https://a.example'] })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

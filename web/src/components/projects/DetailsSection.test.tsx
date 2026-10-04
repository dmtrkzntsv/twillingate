import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { endpoints, type Project } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import DetailsSection from './DetailsSection'

const project = { project_id: 1, name: 'dev', allowed_origins: ['https://a.example', '*'], attributes: ['plan'] } as Project

describe('DetailsSection', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.spyOn(endpoints, 'receivedAttributes')
  })

  it('lists origins one per line, and no breakdowns', () => {
    renderWithProviders(<DetailsSection project={project} onSave={vi.fn()} />)
    const origins = screen.getByRole('list', { name: 'Allowed origins' })
    expect(within(origins).getAllByRole('listitem').map((li) => li.textContent)).toEqual(['https://a.example', '*'])
    expect(screen.queryByText('Breakdowns')).not.toBeInTheDocument()
    expect(endpoints.receivedAttributes).not.toHaveBeenCalled()
  })

  it('says when browsers cannot send', () => {
    renderWithProviders(<DetailsSection project={{ ...project, allowed_origins: [] }} onSave={vi.fn()} />)
    expect(screen.getByText('None: browsers cannot send')).toBeInTheDocument()
  })

  it('edits the name and origins, and sends nothing else', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn().mockResolvedValue(true)
    renderWithProviders(<DetailsSection project={project} onSave={onSave} />)
    await user.click(screen.getByRole('button', { name: 'Edit' }))
    expect(screen.getByRole('heading', { name: 'Edit dev' })).toBeInTheDocument()
    await user.clear(screen.getByLabelText('Name'))
    await user.type(screen.getByLabelText('Name'), 'prod')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledWith({ name: 'prod', allowed_origins: ['https://a.example', '*'] })
  })
})

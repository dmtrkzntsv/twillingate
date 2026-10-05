import { describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Project } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import OriginsSection from './OriginsSection'

const project = { project_id: 1, name: 'dev', allowed_origins: ['https://a.example', 'https://b.example'], attributes: ['plan'] } as Project

function rows() {
  return within(screen.getByRole('table', { name: 'Allowed origins' }))
    .getAllByRole('row')
    .slice(1)
    .map((r) => r.querySelector('code')?.textContent)
}

describe('OriginsSection', () => {
  it('lists origins one per row, each with Remove, and nothing else', () => {
    renderWithProviders(<OriginsSection project={project} onSave={vi.fn()} />)
    expect(rows()).toEqual(['https://a.example', 'https://b.example'])
    expect(screen.getByRole('button', { name: 'Remove https://a.example' })).toBeInTheDocument()
    expect(screen.queryByText('plan')).not.toBeInTheDocument()
  })

  it('says when browsers cannot send', () => {
    renderWithProviders(<OriginsSection project={{ ...project, allowed_origins: [] }} onSave={vi.fn()} />)
    expect(screen.getByText('None: browsers cannot send. Native apps still can.')).toBeInTheDocument()
  })

  it('adds an origin to the end of the list, trimmed', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn().mockResolvedValue(true)
    renderWithProviders(<OriginsSection project={project} onSave={onSave} />)
    await user.click(screen.getByRole('button', { name: 'Add origin' }))
    await user.type(screen.getByLabelText('Origin'), ' https://c.example {Enter}')
    expect(onSave).toHaveBeenCalledWith({ allowed_origins: ['https://a.example', 'https://b.example', 'https://c.example'] })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('refuses an origin already listed, or a blank one', async () => {
    const user = userEvent.setup()
    renderWithProviders(<OriginsSection project={project} onSave={vi.fn()} />)
    await user.click(screen.getByRole('button', { name: 'Add origin' }))
    const add = screen.getByRole('button', { name: 'Add' })
    expect(add).toBeDisabled()
    await user.type(screen.getByLabelText('Origin'), 'https://a.example')
    expect(screen.getByText('Already allowed.')).toBeInTheDocument()
    expect(add).toBeDisabled()
  })

  it('removes one origin after confirming', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn().mockResolvedValue(true)
    renderWithProviders(<OriginsSection project={project} onSave={onSave} />)
    await user.click(screen.getByRole('button', { name: 'Remove https://a.example' }))
    expect(screen.getByRole('alertdialog', { name: 'Stop accepting events from https://a.example?' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Remove origin' }))
    expect(onSave).toHaveBeenCalledWith({ allowed_origins: ['https://b.example'] })
  })

  it('allows everything after confirming, keeping the origins listed', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn().mockResolvedValue(true)
    renderWithProviders(<OriginsSection project={project} onSave={onSave} />)
    await user.click(screen.getByRole('button', { name: 'Allow everything' }))
    expect(screen.getByRole('alertdialog', { name: 'Allow every origin?' })).toBeInTheDocument()
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Allow everything' }))
    expect(onSave).toHaveBeenCalledWith({ allowed_origins: ['https://a.example', 'https://b.example', '*'] })
  })

  it('offers no "Allow everything" once * is listed, and marks * as any origin', () => {
    renderWithProviders(<OriginsSection project={{ ...project, allowed_origins: ['*'] }} onSave={vi.fn()} />)
    expect(screen.queryByRole('button', { name: 'Allow everything' })).not.toBeInTheDocument()
    expect(screen.getByText(/any origin/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Remove *' })).toBeInTheDocument()
  })
})

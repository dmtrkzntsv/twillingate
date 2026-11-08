import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { endpoints, type Project } from '@/lib/api'
import { writeLastForm } from '@/lib/last-form'
import { draft, form } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import FormsLanding from './FormsLanding'

const project: Project = { project_id: 4, name: 'shop', allowed_origins: ['https://shop.example'] }

beforeEach(() => {
  vi.restoreAllMocks()
  localStorage.clear()
  vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [{ project_id: 4, label: 'web', key: 'ak_web_123', state: 'active' }] })
})

function Where() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname + location.search}</output>
}

function renderLanding() {
  return renderWithProviders(
    <MemoryRouter initialEntries={['/projects/1', '/projects/4/forms?range=7d&x=1']} initialIndex={1}>
      <Routes>
        <Route path="/projects/:id/forms" element={<FormsLanding project={project} />} />
        <Route path="*" element={null} />
      </Routes>
      <Where />
    </MemoryRouter>
  )
}

describe('FormsLanding', () => {
  it('lands on the first form, keeping the range', async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [draft('signup', 3), form('contact')] })
    renderLanding()
    expect(await screen.findByText('/projects/4/forms/signup?range=7d')).toBeInTheDocument()
  })

  it('lands on the form last opened on this project', async () => {
    writeLastForm(4, 'contact')
    writeLastForm(5, 'signup')
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [draft('signup', 3), form('contact')] })
    renderLanding()
    expect(await screen.findByText('/projects/4/forms/contact?range=7d')).toBeInTheDocument()
  })

  it('lands on the first when the remembered form is gone', async () => {
    writeLastForm(4, 'archived-one')
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [form('contact'), form('survey')] })
    renderLanding()
    expect(await screen.findByText('/projects/4/forms/contact?range=7d')).toBeInTheDocument()
  })

  it('shows New form and the snippets, on the collector the API names, with no forms', async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ forms: [], action_base: 'https://t.example.com/ingest/forms' })
    renderLanding()
    expect(await screen.findByRole('region', { name: 'Add a form' })).toBeInTheDocument()
    expect(screen.getByText('No forms yet')).toBeInTheDocument()
    expect(screen.getByText(/appears here by itself with its first submission, as a draft/)).toBeInTheDocument()
    expect(screen.getByText(/<form data-twillingate-form="contact">/)).toBeInTheDocument()
    expect(await screen.findByText('https://t.example.com/ingest/forms/contact?key=ak_web_123')).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent('/projects/4/forms?range=7d&x=1')
  })

  it('falls back to a placeholder collector when the server has no INGEST_URL', async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ forms: [], action_base: '' })
    renderLanding()
    expect(await screen.findByText('https://<collector>/ingest/forms/contact?key=ak_web_123')).toBeInTheDocument()
  })

  it('opens New form from the empty state', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ forms: [], action_base: '' })
    renderLanding()
    await user.click(await screen.findByRole('button', { name: 'New form' }))
    expect(await screen.findByRole('dialog', { name: 'New form' })).toBeInTheDocument()
  })
})

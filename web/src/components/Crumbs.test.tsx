import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import Crumbs from './Crumbs'

describe('Crumbs', () => {
  it('links every crumb but the last, which is the page itself', () => {
    render(
      <MemoryRouter>
        <Crumbs items={[{ label: 'Gallery', to: '/gallery/components' }, { label: 'Dashboards', to: '/gallery/dashboards' }]} />
      </MemoryRouter>
    )
    const nav = screen.getByRole('navigation', { name: 'breadcrumb' })
    expect(within(nav).getByRole('link', { name: 'Gallery' })).toHaveAttribute('href', '/gallery/components')
    // The page itself is a disabled link (shadcn's BreadcrumbPage): no href, nowhere to go.
    const page = within(nav).getByText('Dashboards')
    expect(page).toHaveAttribute('aria-current', 'page')
    expect(page).toHaveAttribute('aria-disabled', 'true')
    expect(page).not.toHaveAttribute('href')
  })

  it('goes to the crumb it is clicked on', async () => {
    render(
      <MemoryRouter initialEntries={['/projects/4']}>
        <Routes>
          <Route path="/projects/:id" element={<Crumbs items={[{ label: 'Projects', to: '/projects' }, { label: 'dev' }]} />} />
          <Route path="/projects" element={<p>the list</p>} />
        </Routes>
      </MemoryRouter>
    )
    await userEvent.click(screen.getByRole('link', { name: 'Projects' }))
    expect(screen.getByText('the list')).toBeInTheDocument()
  })
})

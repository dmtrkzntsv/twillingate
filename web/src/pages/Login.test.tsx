import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as auth from '@/lib/auth'
import Login from './Login'

function renderLogin() {
  render(
    <MemoryRouter initialEntries={['/login?returnTo=/dashboards/4']}>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/dashboards/4" element={<p>dashboard 4</p>} />
      </Routes>
    </MemoryRouter>
  )
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('Login', () => {
  it('takes a pasted token and returns to the page asked for', async () => {
    vi.spyOn(auth, 'detectAuth').mockResolvedValue('paste')
    const paste = vi.spyOn(auth, 'setPastedToken').mockImplementation(() => {})
    renderLogin()

    const input = await screen.findByLabelText('API token')
    const submit = screen.getByRole('button', { name: 'Sign in' })
    expect(submit).toBeDisabled()
    await userEvent.type(input, '  tok-1 ')
    await userEvent.click(submit)

    expect(paste).toHaveBeenCalledWith('tok-1')
    expect(await screen.findByText('dashboard 4')).toBeInTheDocument()
  })

  it('says why sign-in did not start and offers a retry', async () => {
    vi.spyOn(auth, 'detectAuth').mockRejectedValue(new Error('network down'))
    renderLogin()

    expect(await screen.findByText('network down')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })
})

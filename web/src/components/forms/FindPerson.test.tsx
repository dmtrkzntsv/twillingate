import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { endpoints } from '@/lib/api'
import { found } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import FindPerson from './FindPerson'

beforeEach(() => vi.restoreAllMocks())

describe('FindPerson', () => {
  it('searches from two characters on and lists what it finds by form', async () => {
    const user = userEvent.setup()
    const find = vi.spyOn(endpoints, 'findSubmissions').mockResolvedValue({
      submissions: [
        found('s1', { form: 'contact', fields: { email: 'ann@example.com', message: 'Hello' } }),
        found('s2', { form: 'signup', fields: { email: 'ann@example.com' } }),
        found('s3', { form: 'contact', fields: { email: 'ann@example.com', message: 'Again' } }),
      ],
    })
    renderWithProviders(<FindPerson projectId={4} />)
    const box = screen.getByRole('searchbox', { name: 'Find a person' })
    await user.type(box, 'a')
    expect(screen.getByRole('button', { name: 'Find' })).toBeDisabled()
    await user.type(box, 'nn@')
    await user.click(screen.getByRole('button', { name: 'Find' }))
    await waitFor(() => expect(find).toHaveBeenCalledWith(4, { search: 'ann@' }))
    const results = await screen.findByRole('region', { name: 'Found submissions' })
    expect(within(results).getByText(/3 submissions/)).toBeInTheDocument()
    const groups = within(results).getAllByRole('heading', { level: 3 })
    expect(groups.map((h) => h.textContent)).toEqual(['contact', 'signup'])
    expect(within(results).getByText(/Again/)).toBeInTheDocument()
  })

  it('marks archived submissions, which Archive all covers too', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'findSubmissions').mockResolvedValue({
      submissions: [
        found('s1', { form: 'old', archived: true }),
        found('s2', { form: 'contact' }),
      ],
    })
    renderWithProviders(<FindPerson projectId={4} />)
    await user.type(screen.getByRole('searchbox', { name: 'Find a person' }), 'bob{Enter}')
    const results = await screen.findByRole('region', { name: 'Found submissions' })
    const groups = within(results).getAllByRole('heading', { level: 3 })
    expect(groups.map((h) => h.textContent)).toEqual(['old', 'contact'])
    const items = within(results).getAllByRole('listitem')
    expect(within(items[0]).getByText('Archived')).toBeInTheDocument()
    expect(within(items[1]).queryByText('Archived')).toBeNull()
    await user.click(within(results).getByRole('button', { name: 'Archive all' }))
    const confirm = await screen.findByRole('alertdialog', { name: /Archive 2 submissions/ })
    expect(within(confirm).getByText(/deletes it for good/)).toBeInTheDocument()
  })

  it('archives every submission found once confirmed', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'findSubmissions').mockResolvedValue({ submissions: [found('s1')] })
    const archive = vi.spyOn(endpoints, 'archiveSubmissions').mockResolvedValue({ archived: 1 })
    renderWithProviders(<FindPerson projectId={4} />)
    await user.type(screen.getByRole('searchbox', { name: 'Find a person' }), 'bob{Enter}')
    const results = await screen.findByRole('region', { name: 'Found submissions' })
    await user.click(within(results).getByRole('button', { name: 'Archive all' }))
    const confirm = await screen.findByRole('alertdialog', { name: /Archive 1 submission\b/ })
    expect(archive).not.toHaveBeenCalled()
    await user.click(within(confirm).getByRole('button', { name: 'Archive submissions' }))
    await waitFor(() => expect(archive).toHaveBeenCalledWith(4, { search: 'bob' }))
  })

  it('says when nothing matches, offering no archive', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'findSubmissions').mockResolvedValue({ submissions: [] })
    renderWithProviders(<FindPerson projectId={4} />)
    await user.type(screen.getByRole('searchbox', { name: 'Find a person' }), 'zed{Enter}')
    expect(await screen.findByText(/No submissions contain “zed”/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Archive all' })).toBeNull()
  })

  it('counts a first page with more after it as a floor', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'findSubmissions').mockResolvedValue({ submissions: [found('s1')], next_cursor: 'c2' })
    renderWithProviders(<FindPerson projectId={4} />)
    await user.type(screen.getByRole('searchbox', { name: 'Find a person' }), 'bob{Enter}')
    expect(await screen.findByText(/1\+ submissions/)).toBeInTheDocument()
  })
})

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { endpoints } from '@/lib/api'
import { submission } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import SubmissionDrawer from './SubmissionDrawer'

beforeEach(() => vi.restoreAllMocks())

describe('SubmissionDrawer', () => {
  it('shows every stored field, then when it arrived and where from', async () => {
    const get = vi.spyOn(endpoints, 'submission').mockResolvedValue(submission('s2'))
    renderWithProviders(<SubmissionDrawer projectId={4} form="contact" id="s2" onClose={() => {}} />)
    const drawer = await screen.findByRole('dialog', { name: 'Submission' })
    expect(get).toHaveBeenCalledWith(4, 'contact', 's2')
    const fields = await within(drawer).findByRole('region', { name: 'Fields' })
    // phone is stored though no longer expected: the drawer shows it too.
    for (const v of ['bob@example.com', 'Hi there', '555-0100']) expect(within(fields).getByText(v)).toBeInTheDocument()
    const arrival = within(drawer).getByRole('region', { name: 'Arrival' })
    expect(within(arrival).getByText('news / email / fall · google.com')).toBeInTheDocument()
    expect(arrival.querySelector('time[datetime="2026-10-04T09:00:00Z"]')).not.toBeNull()
  })

  it('shows the parts of the source it has, a dash with none', async () => {
    vi.spyOn(endpoints, 'submission').mockResolvedValue(submission('s2', { utm_medium: '', utm_campaign: '', referrer: '' }))
    const { unmount } = renderWithProviders(<SubmissionDrawer projectId={4} form="contact" id="s2" onClose={() => {}} />)
    expect(await screen.findByText('news')).toBeInTheDocument()
    unmount()
    vi.spyOn(endpoints, 'submission').mockResolvedValue(
      submission('s3', { utm_source: '', utm_medium: '', utm_campaign: '', referrer: '' }),
    )
    renderWithProviders(<SubmissionDrawer projectId={4} form="contact" id="s3" onClose={() => {}} />)
    const arrival = await screen.findByRole('region', { name: 'Arrival' })
    expect(within(arrival).getByText('—')).toBeInTheDocument()
  })

  it('deletes the submission once confirmed, then closes', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'submission').mockResolvedValue(submission('s2'))
    const del = vi.spyOn(endpoints, 'deleteSubmissions').mockResolvedValue({ deleted: 1 })
    const onClose = vi.fn()
    renderWithProviders(<SubmissionDrawer projectId={4} form="contact" id="s2" onClose={onClose} />)
    const drawer = await screen.findByRole('dialog', { name: 'Submission' })
    await within(drawer).findByText('bob@example.com')
    await user.click(within(drawer).getByRole('button', { name: 'Delete' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Delete this submission?' })
    await user.click(within(confirm).getByRole('button', { name: 'Delete submission' }))
    await waitFor(() => expect(del).toHaveBeenCalledWith(4, { ids: ['s2'] }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it('is closed without an id', () => {
    renderWithProviders(<SubmissionDrawer projectId={4} form="contact" id={undefined} onClose={() => {}} />)
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

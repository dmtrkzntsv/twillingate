import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { endpoints, type Widget, type WidgetShare } from '@/lib/api'
import { widgets } from '@/components/widgets'
import { renderWithProviders } from '@/test/render'
import { ShareDialog } from './ShareDialog'

vi.mock('@/lib/capture', () => ({
  captureCard: vi.fn(),
  downloadBlob: vi.fn(),
}))
import { captureCard } from '@/lib/capture'

const widget: Widget = {
  widget_id: 42,
  dashboard_id: 1,
  name: 'visitors',
  component: 'stat',
  title: 'Visitors',
  width: 3,
  height: 3,
  props: widgets.stat.examples[0].props,
  source: { type: 'sql', content: 'SELECT 1' },
  follows_project: true,
  follows_range: true,
}
const share = {
  project: { id: 7, name: 'blog' },
  from: '2026-09-05',
  to: '2026-10-04',
  rangeShown: true,
  writable: true,
}

const created: WidgetShare = {
  id: '0190a0a0-0000-7000-8000-000000000001',
  url: 'https://t.example/share/0190a0a0-0000-7000-8000-000000000001',
  image_url: 'https://t.example/share/0190a0a0-0000-7000-8000-000000000001.png',
  image_2x_url: 'https://t.example/share/0190a0a0-0000-7000-8000-000000000001@2x.png',
  widget_id: 42,
  dashboard_id: 1,
  dashboard_title: 'Overview',
  project_id: 7,
  project_name: 'blog',
  from: '2026-09-05',
  to: '2026-10-04',
  title: 'Visitors',
  created_at: '2026-10-06T10:00:00Z',
  archive_at: '2026-11-05T10:00:00Z',
  archived_at: null,
  caption_project: true,
  caption_range: true,
}

function dialog(w: Widget = widget) {
  return renderWithProviders(
    <MemoryRouter>
      <ShareDialog open onOpenChange={() => {}} widget={w} data={widgets.stat.examples[0].data} share={share} />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  vi.mocked(captureCard).mockResolvedValue({
    image: new Blob(['1x'], { type: 'image/png' }),
    image2x: new Blob(['2x'], { type: 'image/png' }),
  })
  URL.createObjectURL = vi.fn(() => 'blob:preview')
  URL.revokeObjectURL = vi.fn()
  vi.spyOn(endpoints, 'widgetShares').mockResolvedValue({ shares: [] })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.mocked(captureCard).mockReset()
})

describe('ShareDialog', () => {
  it('previews the captured card and defaults to one month', async () => {
    dialog()
    const img = await screen.findByRole('img')
    expect(img).toHaveAttribute('src', 'blob:preview')
    expect(screen.getByLabelText('Archive after')).toHaveValue('30d')
    expect(screen.getByRole('option', { name: '1 month' })).toBeInTheDocument()
    expect(screen.getByText('Feeds keep the preview they already fetched.')).toBeInTheDocument()
  })

  it('waits for the capture before Create link is enabled', async () => {
    let done!: (v: { image: Blob; image2x: Blob }) => void
    vi.mocked(captureCard).mockReturnValue(new Promise((r) => (done = r)))
    dialog()
    expect(screen.getByRole('button', { name: 'Create link' })).toBeDisabled()
    done({ image: new Blob(['1x']), image2x: new Blob(['2x']) })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create link' })).toBeEnabled())
  })

  it('creates the link from the form and shows it with its actions', async () => {
    const user = userEvent.setup()
    const create = vi.spyOn(endpoints, 'createWidgetShare').mockResolvedValue(created)
    dialog()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create link' })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: 'Create link' }))

    const form = create.mock.calls[0][0]
    expect(form.get('widget_id')).toBe('42')
    expect(form.get('project_id')).toBe('7')
    expect(form.get('from')).toBe('2026-09-05')
    expect(form.get('to')).toBe('2026-10-04')
    expect(form.get('archive_after')).toBe('30d')
    expect(form.get('caption_project')).toBe('1')
    expect(form.get('caption_range')).toBe('1')
    expect((form.get('image') as File).name).toBe('image.png')
    expect((form.get('image') as File).type).toBe('image/png')
    expect(form.get('image_2x')).toBeInstanceOf(File)

    expect(await screen.findByDisplayValue(created.url)).toHaveAttribute('readonly')
    expect(screen.getByRole('button', { name: 'Copy link' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Copy embed code' })).toBeInTheDocument()
    const open = screen.getByRole('link', { name: 'Open' })
    expect(open).toHaveAttribute('href', created.url)
    expect(open).toHaveAttribute('target', '_blank')
    expect(open).toHaveAttribute('rel', 'noopener')
    expect(screen.queryByRole('button', { name: 'Create link' })).not.toBeInTheDocument()
  })

  it('captions only what the widget follows, on the card and in the upload', async () => {
    const user = userEvent.setup()
    const create = vi.spyOn(endpoints, 'createWidgetShare').mockResolvedValue(created)
    let drawn = ''
    vi.mocked(captureCard).mockImplementation(async (node) => {
      drawn = node.querySelector('[data-share-meta]')?.textContent ?? '(none)'
      return { image: new Blob(['1x']), image2x: new Blob(['2x']) }
    })
    dialog({ ...widget, follows_range: false })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create link' })).toBeEnabled())
    expect(drawn).toBe('blog')
    await user.click(screen.getByRole('button', { name: 'Create link' }))
    const form = create.mock.calls[0][0]
    expect(form.get('caption_project')).toBe('1')
    expect(form.get('caption_range')).toBe('0')
    // The range is still sent: it dates the share and names its file.
    expect(form.get('from')).toBe('2026-09-05')
  })

  it('sends the archive choice', async () => {
    const user = userEvent.setup()
    const create = vi.spyOn(endpoints, 'createWidgetShare').mockResolvedValue(created)
    dialog()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create link' })).toBeEnabled())
    await user.selectOptions(screen.getByLabelText('Archive after'), 'project')
    await user.click(screen.getByRole('button', { name: 'Create link' }))
    expect(create.mock.calls[0][0].get('archive_after')).toBe('project')
  })

  it("links to the widget's other shares", async () => {
    vi.spyOn(endpoints, 'widgetShares').mockResolvedValue({
      shares: [
        { ...created, id: 'a' },
        { ...created, id: 'b' },
      ],
    })
    dialog()
    const link = await screen.findByRole('link', {
      name: /This widget has 2 other links/,
    })
    expect(link).toHaveAttribute('href', '/shares?widget=42')
  })

  it('shows a fresh form, not the old link, when reopened after closing mid-create', async () => {
    const user = userEvent.setup()
    let sent!: (s: WidgetShare) => void
    vi.spyOn(endpoints, 'createWidgetShare').mockReturnValue(new Promise((r) => (sent = r)))
    function Harness() {
      const [open, setOpen] = useState(true)
      return (
        <MemoryRouter>
          <button onClick={() => setOpen(true)}>Reopen</button>
          <ShareDialog open={open} onOpenChange={setOpen} widget={widget} data={widgets.stat.examples[0].data} share={share} />
        </MemoryRouter>
      )
    }
    renderWithProviders(<Harness />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create link' })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: 'Create link' }))
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await act(async () => sent(created))

    await user.click(screen.getByRole('button', { name: 'Reopen' }))
    await screen.findByRole('img', { name: 'Preview of the share card' })
    expect(screen.queryByDisplayValue(created.url)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create link' })).toBeInTheDocument()
    // Drawn and captured again for this opening.
    expect(captureCard).toHaveBeenCalledTimes(2)
  })
})

import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { endpoints, type Widget, type WidgetShare } from '@/lib/api'
import { widgets } from '@/components/widgets'
import { embedCode } from '@/lib/share'
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

const CREATE = 'Create and copy link'

/** The enabled create button, once the card is captured. */
async function createButton() {
  const b = screen.getByRole('button', { name: CREATE })
  await waitFor(() => expect(b).toBeEnabled())
  return b
}

function setClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true })
}

describe('ShareDialog', () => {
  it('previews the captured card, says what a share is in one line and defaults to one month', async () => {
    dialog()
    const img = await screen.findByRole('img')
    expect(img).toHaveAttribute('src', 'blob:preview')
    expect(screen.getByText('Anyone with the link sees this picture. It never updates.')).toBeInTheDocument()
    expect(screen.getByLabelText('Archive after')).toHaveValue('30d')
    expect(screen.getByRole('option', { name: '1 month' })).toBeInTheDocument()
    // Nothing to copy or open before the link exists.
    expect(screen.queryByRole('button', { name: 'Copy embed code' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Open' })).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('waits for the capture before the link can be created', async () => {
    let done!: (v: { image: Blob; image2x: Blob }) => void
    vi.mocked(captureCard).mockReturnValue(new Promise((r) => (done = r)))
    dialog()
    expect(screen.getByRole('button', { name: CREATE })).toBeDisabled()
    done({ image: new Blob(['1x']), image2x: new Blob(['2x']) })
    await createButton()
  })

  it('creates the link from the form, copies it, and offers the embed code and Open in place', async () => {
    const user = userEvent.setup()
    const create = vi.spyOn(endpoints, 'createWidgetShare').mockResolvedValue(created)
    dialog()
    await user.click(await createButton())

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

    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument()
    expect(await navigator.clipboard.readText()).toBe(created.url)
    expect(screen.getByRole('button', { name: 'Copy embed code' }).querySelector('svg')).toHaveClass('lucide-code')
    const open = screen.getByRole('link', { name: 'Open' })
    expect(open).toHaveAttribute('href', created.url)
    expect(open).toHaveAttribute('target', '_blank')
    expect(open).toHaveAttribute('rel', 'noopener')
    // The link is copied, not shown.
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    // Then it is a plain copy button.
    expect(await screen.findByRole('button', { name: 'Copy link' }, { timeout: 3000 })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: CREATE })).not.toBeInTheDocument()
  })

  it('shows the link and embed code to select by hand when the clipboard is not there', async () => {
    const user = userEvent.setup()
    setClipboard(undefined)
    vi.spyOn(endpoints, 'createWidgetShare').mockResolvedValue(created)
    dialog()
    await user.click(await createButton())
    expect(await screen.findByRole('button', { name: "Couldn't copy" })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Share link' })).toHaveValue(created.url)
    expect(screen.getByRole('textbox', { name: 'Share link' })).toHaveAttribute('readonly')
    expect(screen.getByRole('textbox', { name: 'Embed code' })).toHaveValue(embedCode(created))
  })

  it('stays ready to try again, copying nothing, when the link is refused', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'createWidgetShare').mockRejectedValue(new Error('boom'))
    dialog()
    await user.click(await createButton())
    await createButton()
    expect(await navigator.clipboard.readText()).toBe('')
    expect(screen.queryByRole('link', { name: 'Open' })).not.toBeInTheDocument()
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
    const button = await createButton()
    expect(drawn).toBe('blog')
    await user.click(button)
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
    const button = await createButton()
    await user.selectOptions(screen.getByLabelText('Archive after'), 'project')
    await user.click(button)
    expect(create.mock.calls[0][0].get('archive_after')).toBe('project')
  })

  it('changes the archive date of the link it made', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'createWidgetShare').mockResolvedValue(created)
    const update = vi.spyOn(endpoints, 'updateWidgetShare').mockResolvedValue({ ...created, archive_at: '2027-01-04T10:00:00Z' })
    dialog()
    await user.click(await createButton())
    await screen.findByRole('link', { name: 'Open' })
    await user.selectOptions(screen.getByLabelText('Archive after'), '90d')
    expect(update).toHaveBeenCalledWith(created.id, '90d')
    await waitFor(() => expect(screen.getByLabelText('Archive after')).toHaveValue('90d'))
  })

  it("links to the widget's other shares", async () => {
    vi.spyOn(endpoints, 'widgetShares').mockResolvedValue({
      shares: [
        { ...created, id: 'a' },
        { ...created, id: 'b' },
      ],
    })
    dialog()
    const link = await screen.findByRole('link', { name: '2 other links' })
    expect(link).toHaveAttribute('href', '/shares?widget=42')
  })

  it('shows a fresh form, and copies nothing, when reopened after closing mid-create', async () => {
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
    await user.click(await createButton())
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await act(async () => sent(created))
    expect(await navigator.clipboard.readText()).toBe('')

    await user.click(screen.getByRole('button', { name: 'Reopen' }))
    await screen.findByRole('img', { name: 'Preview of the share card' })
    expect(screen.queryByRole('link', { name: 'Open' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: CREATE })).toBeInTheDocument()
    // Drawn and captured again for this opening.
    expect(captureCard).toHaveBeenCalledTimes(2)
  })
})

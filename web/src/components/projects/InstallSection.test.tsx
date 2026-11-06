import { describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import type { IngestKey, Project } from '@/lib/api'
import InstallSection from './InstallSection'

const project: Project = { project_id: 4, name: 'econumo.com', allowed_origins: ['https://econumo.com'] }
const key = (label: string, state: IngestKey['state']): IngestKey => ({
  project_id: 4,
  label,
  key: `ak_${label}`,
  state,
  snippet: `<script src="https://t.example/js/twillingate.js" data-key="ak_${label}"></script>`,
})

async function open(props: Partial<Parameters<typeof InstallSection>[0]> = {}) {
  render(
    <MemoryRouter>
      <InstallSection project={project} keys={[key('web', 'active')]} domainsPath="/projects/4/settings/domains?range=30d" {...props} />
    </MemoryRouter>
  )
  // The snippet waits behind the button.
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Show install snippet' }))
  return screen.getByRole('dialog')
}

describe('InstallSection', () => {
  it('shows the snippet behind a button, copyable', async () => {
    const dialog = await open()
    expect(within(dialog).getByText(/data-key="ak_web"/)).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Copy snippet' })).toBeInTheDocument()
    expect(within(dialog).queryByText(/No allowed origins/)).not.toBeInTheDocument()
  })

  it('uses the first active key, and says which', async () => {
    const dialog = await open({ keys: [key('old', 'disabled'), key('web', 'active'), key('ios', 'active')] })
    expect(within(dialog).getByText(/data-key="ak_web"/)).toBeInTheDocument()
    expect(within(dialog).getByText(/uses the key/)).toHaveTextContent('web')
  })

  it('says there is nothing to paste without an active key', async () => {
    const dialog = await open({ keys: [key('old', 'disabled')] })
    expect(within(dialog).getByText(/no active key/i)).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: 'Copy snippet' })).not.toBeInTheDocument()
  })

  it('warns that browsers are refused without allowed origins', async () => {
    const dialog = await open({ project: { ...project, allowed_origins: [] } })
    expect(within(dialog).getByText(/No allowed origins/)).toBeInTheDocument()
    expect(within(dialog).getByText(/data-key="ak_web"/)).toBeInTheDocument()
    expect(within(dialog).getByRole('link', { name: 'Settings → Domains' })).toHaveAttribute('href', '/projects/4/settings/domains?range=30d')
  })

  it("passes on the server's note about a placeholder collector", async () => {
    const dialog = await open({ note: 'INGEST_URL is not configured' })
    expect(within(dialog).getByText('INGEST_URL is not configured')).toBeInTheDocument()
  })

  it('offers a retry when the keys could not be read', async () => {
    const onRetry = vi.fn()
    const dialog = await open({ keys: undefined, error: new Error('boom'), onRetry })
    await userEvent.click(within(dialog).getByRole('button', { name: /retry/i }))
    expect(onRetry).toHaveBeenCalled()
  })
})

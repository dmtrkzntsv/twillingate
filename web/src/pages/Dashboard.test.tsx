import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, endpoints, type Project } from '@/lib/api'
import { span } from '@/lib/grid'
import { answerFor, dashboardsList, details, projects, views, widgetsById } from '@/test/fixtures'
import { renderWithProviders } from '@/test/render'
import Dashboard from './Dashboard'

function LocationProbe() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname + location.search}</output>
}

function mockApi(opts: { projects?: Project[]; dev?: boolean } = {}) {
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue(
    dashboardsList(opts.dev ? { dev: true, errors: [{ dir: 'dashboards/sales', message: 'widget "x": unknown component' }] } : {})
  )
  vi.spyOn(endpoints, 'dashboard').mockImplementation(async (id) => {
    const d = details[id]
    if (!d) throw new ApiError(404, 'no such dashboard', 'not_found')
    return d
  })
  vi.spyOn(endpoints, 'projects').mockResolvedValue({ projects: opts.projects ?? projects })
  vi.spyOn(endpoints, 'devVersion').mockResolvedValue({ version: 'v1' })
  vi.spyOn(endpoints, 'saveView').mockResolvedValue({ status: 'saved' })
  return vi.spyOn(endpoints, 'widgetData').mockImplementation(async (id) => answerFor(widgetsById.get(id)!))
}

function renderAt(url: string) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route
          path="/dashboards/:id"
          element={
            <>
              <Dashboard />
              <LocationProbe />
            </>
          }
        />
      </Routes>
    </MemoryRouter>
  )
}

const location = () => screen.getByTestId('location').textContent

beforeEach(() => {
  localStorage.clear()
  window.innerWidth = 1280
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('Dashboard', () => {
  it('renders a system dashboard as report tabs with both switchers', async () => {
    mockApi()
    renderAt('/dashboards/2?project=7&range=7d')

    const tabs = await screen.findAllByRole('tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['Views', 'Product', 'Users', 'Groups', 'Retention'])
    expect(screen.getByRole('tab', { name: 'Product' })).toHaveAttribute('aria-selected', 'true')
    expect(await screen.findByRole('button', { name: 'Project: shop' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Range: Last week' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh all' })).toBeInTheDocument()
  })

  it('lists Reports and the live user dashboards in the sidebar', async () => {
    mockApi()
    renderAt('/dashboards/1')

    const reports = await screen.findByRole('link', { name: 'Reports' })
    expect(reports).toHaveAttribute('href', '/dashboards/1')
    expect(screen.getByRole('link', { name: 'Launch week' })).toHaveAttribute('href', '/dashboards/10')
    expect(screen.queryByRole('link', { name: 'Old experiment' })).not.toBeInTheDocument()
  })

  it('renders a user dashboard with only fixed widgets without switchers or tabs', async () => {
    const widgetData = mockApi()
    renderAt('/dashboards/10')

    expect(await screen.findByRole('heading', { level: 1, name: 'Launch week' })).toBeInTheDocument()
    expect(await screen.findByText('Signups during launch')).toBeInTheDocument()
    expect(screen.queryByRole('tab')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Project/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Range/ })).not.toBeInTheDocument()
    await waitFor(() => expect(widgetData).toHaveBeenCalled())
    for (const call of widgetData.mock.calls) expect(call[1]).toEqual({})
  })

  // The switcher tests use the small Product report: the Views one renders
  // every chart kind, which is slow to re-render on each interaction.
  it('saves a new range and puts it in the URL', async () => {
    const widgetData = mockApi()
    renderAt('/dashboards/2?project=7&range=7d')

    await userEvent.click(await screen.findByRole('button', { name: 'Range: Last week' }))
    await userEvent.click(await screen.findByRole('menuitemradio', { name: 'Last month' }))

    await waitFor(() => expect(endpoints.saveView).toHaveBeenCalledWith(2, { project_id: 7, range: '30d' }))
    expect(location()).toBe('/dashboards/2?project=7&range=30d')
    expect(screen.getByRole('button', { name: 'Range: Last month' })).toBeInTheDocument()
    const days = (call: unknown[]) => {
      const q = call[1] as { from: string; to: string }
      return (Date.parse(q.to) - Date.parse(q.from)) / 86_400_000
    }
    await waitFor(() => expect(widgetData.mock.calls.map(days)).toContain(29))
  })

  it('saves a new project and puts it in the URL', async () => {
    mockApi()
    renderAt('/dashboards/2?project=7&range=7d')

    await userEvent.click(await screen.findByRole('button', { name: 'Project: shop' }))
    await userEvent.click(await screen.findByRole('menuitemradio', { name: 'blog' }))

    await waitFor(() => expect(endpoints.saveView).toHaveBeenCalledWith(2, { project_id: 1, range: '7d' }))
    expect(location()).toBe('/dashboards/2?project=1&range=7d')
  })

  it('saves a custom range picked on the calendar', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-09-26T12:00:00Z'))
    mockApi()
    renderAt('/dashboards/2?project=7&range=7d')

    await userEvent.click(await screen.findByRole('button', { name: 'Range: Last week' }))
    await userEvent.click(await screen.findByRole('menuitem', { name: 'Custom…' }))
    const picker = await screen.findByRole('dialog')
    await userEvent.click(within(picker).getByRole('button', { name: /September 10(th)?, 2026/ }))
    await userEvent.click(within(picker).getByRole('button', { name: /September 20(th)?, 2026/ }))
    await userEvent.click(within(picker).getByRole('button', { name: 'Apply' }))

    await waitFor(() =>
      expect(endpoints.saveView).toHaveBeenCalledWith(2, {
        project_id: 7,
        range: 'custom',
        from: '2026-09-10',
        to: '2026-09-20',
      })
    )
    expect(location()).toBe('/dashboards/2?project=7&range=custom&from=2026-09-10&to=2026-09-20')
    vi.useRealTimers()
  })

  it('carries the selection to the next report tab and saves it there', async () => {
    mockApi()
    renderAt('/dashboards/2?project=1&range=30d')

    await userEvent.click(await screen.findByRole('tab', { name: 'Users' }))

    await waitFor(() => expect(location()).toBe('/dashboards/3?project=1&range=30d'))
    await waitFor(() => expect(endpoints.saveView).toHaveBeenCalledWith(3, { project_id: 1, range: '30d' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Users' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Users' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('button', { name: 'Range: Last month' })).toBeInTheDocument()
  })

  it('asks for a project first when there are no active ones, and loads no widget', async () => {
    const widgetData = mockApi({ projects: [{ project_id: 3, name: 'legacy', archived: true }] })
    renderAt('/dashboards/1')

    expect(await screen.findByText('Create a project first')).toBeInTheDocument()
    expect(screen.getByText(/create_project/)).toBeInTheDocument()
    expect(widgetData).not.toHaveBeenCalled()
  })

  it('says so when a dashboard has no widgets', async () => {
    mockApi()
    renderAt('/dashboards/12')
    expect(await screen.findByText('No widgets yet — ask your agent to add some')).toBeInTheDocument()
  })

  it('shows the oldest data time on screen', async () => {
    mockApi()
    renderAt('/dashboards/10')
    expect(await screen.findByText(/^Data as of /)).toBeInTheDocument()
  })

  it('lists the dev errors in a banner and polls the dev version', async () => {
    mockApi({ dev: true })
    renderAt('/dashboards/10')
    expect(await screen.findByText(/unknown component/)).toBeInTheDocument()
    expect(screen.getByText(/dashboards\/sales/)).toBeInTheDocument()
    await waitFor(() => expect(endpoints.devVersion).toHaveBeenCalled())
  })

  it('reloads the page when the dev version changes', async () => {
    mockApi({ dev: true })
    vi.mocked(endpoints.devVersion).mockResolvedValueOnce({ version: 'v1' }).mockResolvedValue({ version: 'v2' })
    const reload = vi.fn()
    vi.stubGlobal('location', { ...window.location, reload })
    renderAt('/dashboards/10')
    await waitFor(() => expect(reload).toHaveBeenCalled(), { timeout: 3000 })
  })
})

// The grid reads its own width from a ResizeObserver; play one that reports
// a fixed width for the grid (and nothing for the charts inside it).
function observeGridAt(width: number) {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      cb: ResizeObserverCallback
      constructor(cb: ResizeObserverCallback) {
        this.cb = cb
      }
      observe(el: Element) {
        if (el.getAttribute('data-slot') === 'widget-grid') {
          this.cb([{ contentRect: { width } } as ResizeObserverEntry], this as unknown as ResizeObserver)
        }
      }
      unobserve() {}
      disconnect() {}
    }
  )
}

describe.each([
  { screenWidth: 1280, gridWidth: 1000 },
  { screenWidth: 390, gridWidth: 358 },
])('the Views report at a $gridWidth px grid', ({ screenWidth, gridWidth }) => {
  it('renders every widget kind in a card spanning its adapted width', async () => {
    window.innerWidth = screenWidth
    observeGridAt(gridWidth)
    mockApi()
    renderAt('/dashboards/1?project=7&range=7d')

    const grid = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('[data-slot=widget-grid]')
      expect(el).not.toBeNull()
      return el!
    })
    for (const w of views.widgets) expect(within(grid).getByRole('heading', { name: w.title })).toBeInTheDocument()
    await screen.findByText(/^Data as of/)

    const cells = Array.from(grid.children) as HTMLElement[]
    expect(cells.map((c) => Number(c.dataset.span))).toEqual(views.widgets.map((w) => span(w.width, gridWidth)))
    expect(cells.map((c) => c.style.gridRow)).toEqual(views.widgets.map((w) => `span ${w.height} / span ${w.height}`))

    for (const cell of cells) {
      await waitFor(() => expect(cell.querySelector('[data-slot=skeleton]')).toBeNull())
      const card = within(cell)
      expect(card.queryByText("Couldn't load")).toBeNull()
      expect(card.queryByText('Query no longer runs')).toBeNull()
      expect(card.queryByText('Component removed')).toBeNull()
      expect(card.queryByText('No data for this range')).toBeNull()
    }
    await act(async () => {})
  }, 20_000)
})

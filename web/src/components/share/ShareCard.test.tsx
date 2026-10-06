import { StrictMode } from 'react'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { widgets } from '@/components/widgets'
import type { SqlData } from '@/components/widgets/types'
import { CARD_ROWS } from '@/components/widgets/table'
import { OffscreenCard } from './OffscreenCard'
import { ShareCard, type ShareCardProps } from './ShareCard'

function card(over: Partial<ShareCardProps> & Pick<ShareCardProps, 'component' | 'data' | 'props'>) {
  return render(<ShareCard title="Visitors" projectName="blog" from="2026-09-05" to="2026-10-04" {...over} />)
}

const stat = widgets.stat.examples[0]
const table = widgets.table.examples[0]

/** Twelve rows, four more than a card shows. */
const twelve: SqlData = {
  columns: ['page', 'visitors'],
  rows: Array.from({ length: 12 }, (_, i) => [`/page${i + 1}`, String(100 - i)]),
  truncated: false,
}

describe('ShareCard', () => {
  it('is a fixed 1200×630 card', () => {
    const { container } = card({ component: 'stat', data: stat.data, props: stat.props })
    const root = container.querySelector('[data-share-card]') as HTMLElement
    expect(root).not.toBeNull()
    expect(root).toHaveClass('share-card')
    expect(root.style.width).toBe('1200px')
    expect(root.style.height).toBe('630px')
  })

  it('clamps the title to two lines and names the project and range in words', () => {
    const { container } = card({ component: 'stat', data: stat.data, props: stat.props, title: 'A title' })
    expect(screen.getByRole('heading', { name: 'A title' })).toHaveClass('line-clamp-2')
    expect(container.querySelector('[data-share-meta]')).toHaveTextContent(/^blog · Sep 5 – Oct 4, 2026$/)
  })

  it('carries the watermark: the iceberg and twillingate.dev', () => {
    const { container } = card({ component: 'table', data: table.data, props: table.props })
    const mark = screen.getByText('twillingate.dev')
    expect(mark.querySelector('svg') ?? mark.parentElement?.querySelector('svg')).not.toBeNull()
    expect(container.querySelector('[data-share-card]')).toContainElement(mark)
  })

  it('draws the stat large', () => {
    const { container } = card({ component: 'stat', data: stat.data, props: stat.props })
    expect(container.querySelector('.text-\\[96px\\]')).toHaveTextContent('4,405')
  })

  it('shows the first rows of a table and how many more, with no controls', () => {
    const { container } = card({ component: 'table', data: twelve, props: {} })
    const body = container.querySelector('tbody') as HTMLElement
    const rows = within(body).getAllByRole('row')
    // The eight data rows, then the "and N more" row.
    expect(rows).toHaveLength(CARD_ROWS + 1)
    expect(rows[0]).toHaveTextContent('/page1')
    expect(rows[CARD_ROWS - 1]).toHaveTextContent(`/page${CARD_ROWS}`)
    expect(rows[CARD_ROWS]).toHaveTextContent('and 4 more')
    expect(screen.queryByRole('button', { name: /next/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /filter/i })).toBeNull()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('adds no "more" row when every row fits', () => {
    card({ component: 'table', data: table.data, props: table.props })
    expect(screen.queryByText(/and \d+ more/)).toBeNull()
    expect(screen.getByText('duckduckgo.com')).toBeInTheDocument()
  })

  it('shows no tooltip', () => {
    const line = widgets.line.examples[0]
    const { container } = card({ component: 'line', data: line.data, props: line.props })
    // Recharts mounts the wrapper but keeps it hidden until a pointer hovers,
    // which never happens on an off-screen card.
    const tips = Array.from(container.querySelectorAll<HTMLElement>('.recharts-tooltip-wrapper'))
    expect(tips.length).toBeGreaterThan(0)
    expect(tips.filter((t) => t.style.visibility !== 'hidden')).toHaveLength(0)
  })
})

describe('OffscreenCard', () => {
  const meta = { title: 'Notes', projectName: 'blog', from: '2026-09-05', to: '2026-10-04' }

  it('draws the card out of sight on <body> and hands it over', () => {
    const onNode = vi.fn()
    const stat = widgets.stat.examples[0]
    const { container } = render(
      <OffscreenCard component="stat" data={stat.data} props={stat.props} {...meta} onNode={onNode} />
    )
    const node = document.body.querySelector('[data-share-card]') as HTMLElement
    expect(container).not.toContainElement(node)
    expect(node.parentElement?.style.left).toBe('-10000px')
    expect(onNode).toHaveBeenCalledOnce()
    expect(onNode).toHaveBeenCalledWith(node)
  })

  it('waits for a lazy component to load before handing the card over', async () => {
    const onNode = vi.fn()
    const md = widgets.markdown.examples[0]
    render(<OffscreenCard component="markdown" data={md.data} props={md.props} {...meta} onNode={onNode} />)
    await waitFor(() => expect(onNode).toHaveBeenCalledOnce())
    expect((onNode.mock.calls[0][0] as HTMLElement).textContent).toContain('Weekly notes')
  })
})

describe('ShareCard fonts', () => {
  afterEach(() => {
    // jsdom has no Font Loading API; the tests below lend it one.
    delete (document as { fonts?: unknown }).fonts
  })

  it('draws the widget, and reports ready, only once Inter has loaded', async () => {
    let loaded!: () => void
    const faces = new Promise<void>((resolve) => (loaded = resolve))
    const load = vi.fn(() => faces.then(() => []))
    Object.defineProperty(document, 'fonts', { value: { load, ready: Promise.resolve() }, configurable: true })
    const onReady = vi.fn()
    const { container } = render(
      <ShareCard component="table" data={twelve} props={{}} title="Pages" projectName="blog" from="2026-09-05" to="2026-10-04" onReady={onReady} />
    )
    expect(load).toHaveBeenCalledWith('600 44px Inter', 'Pages')
    expect(load).toHaveBeenCalledWith('400 22px Inter', expect.stringContaining('blog · Sep 5 – Oct 4, 2026'))
    // The title is there to measure; the widget, which measures itself, waits.
    expect(screen.getByRole('heading', { name: 'Pages' })).toBeInTheDocument()
    expect(container.querySelector('table')).toBeNull()
    expect(onReady).not.toHaveBeenCalled()
    await act(async () => loaded())
    expect(container.querySelector('table')).not.toBeNull()
    expect(onReady).toHaveBeenCalledOnce()
  })

  it('draws with the fallback font when a face fails to load', async () => {
    Object.defineProperty(document, 'fonts', {
      value: { load: vi.fn(() => Promise.reject(new Error('offline'))), ready: Promise.resolve() },
      configurable: true,
    })
    const onReady = vi.fn()
    render(<ShareCard component="table" data={twelve} props={{}} title="Pages" projectName="blog" from="2026-09-05" to="2026-10-04" onReady={onReady} />)
    await waitFor(() => expect(onReady).toHaveBeenCalledOnce())
  })
})

describe('OffscreenCard under StrictMode', () => {
  it('hands the card over once, though effects mount twice', () => {
    const onNode = vi.fn()
    const stat = widgets.stat.examples[0]
    render(
      <StrictMode>
        <OffscreenCard component="stat" data={stat.data} props={stat.props} title="Visitors" projectName="blog" from="2026-09-05" to="2026-10-04" onNode={onNode} />
      </StrictMode>
    )
    expect(onNode).toHaveBeenCalledOnce()
  })
})

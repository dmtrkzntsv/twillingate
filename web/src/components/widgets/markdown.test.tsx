import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { MarkdownData, WidgetProps } from './types'
import Markdown, { contract } from './markdown'

function renderMarkdown(data: MarkdownData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 400, height: 100 }}>
      <Markdown data={data} props={props} />
    </div>
  )
}

describe('markdown', () => {
  it('renders formatted markdown', () => {
    const { container } = renderMarkdown({ markdown: '# Title\n\nSome **bold** text.' })
    expect(container.querySelector('h1')?.textContent).toBe('Title')
    expect(container.querySelector('strong')?.textContent).toBe('bold')
  })

  it('renders raw HTML as text, never as an element', () => {
    const { container } = renderMarkdown({ markdown: 'before <script>alert(1)</script> after' })
    expect(container.querySelector('script')).toBeNull()
    expect(container.textContent).toContain('<script>alert(1)</script>')
  })

  it('renders nothing broken for empty markdown', () => {
    const { container } = renderMarkdown({ markdown: '' })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['md'])
    expect(contract.inputs).toEqual({ open: false, columns: [] })
    expect(contract.defaultWidth).toBe(12)
    expect(contract.defaultHeight).toBe(2)
  })
})

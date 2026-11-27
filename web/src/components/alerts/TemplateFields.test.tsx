import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import TemplateFields from './TemplateFields'

function show(props: Partial<React.ComponentProps<typeof TemplateFields>>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <TemplateFields projectId={1} template="conversion" fields={{}} onChange={vi.fn()} {...props} />
    </QueryClientProvider>,
  )
}

describe('TemplateFields errors', () => {
  it('shows a field error under the field it belongs to, and marks the select invalid', () => {
    show({ fields: { from_event: 'visit' }, errors: { to_event: 'Pick an event' } })
    const to = screen.getByLabelText('To event')
    expect(to).toHaveAttribute('aria-invalid', 'true')
    const msg = screen.getByText('Pick an event')
    expect(to.getAttribute('aria-describedby')).toBe(msg.id)
    expect(to.closest('div.flex-col')).toContainElement(msg)
    expect(screen.getByLabelText('From event')).not.toHaveAttribute('aria-invalid')
  })

  it('shows nothing without errors', () => {
    show({})
    expect(screen.queryByText('Pick an event')).toBeNull()
  })
})

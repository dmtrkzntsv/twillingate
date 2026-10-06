import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import CopyButton from './CopyButton'

function setClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true })
}

afterEach(() => vi.useRealTimers())

describe('CopyButton', () => {
  it('writes the value and says Copied only once the write succeeded', async () => {
    const user = userEvent.setup()
    const writeText = vi.fn().mockResolvedValue(undefined)
    setClipboard({ writeText })
    render(<CopyButton value="ak_1" label="Copy key" />)
    await user.click(screen.getByRole('button', { name: 'Copy key' }))
    expect(writeText).toHaveBeenCalledWith('ak_1')
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument()
  })

  it('shows the icon it is given while idle, the copy icon otherwise', () => {
    render(
      <>
        <CopyButton value="a" label="Plain" />
        <CopyButton value="b" label="Linked" icon={<svg data-testid="own" />} />
      </>
    )
    expect(screen.getByRole('button', { name: 'Plain' }).querySelector('svg')).toHaveClass('lucide-copy')
    expect(screen.getByRole('button', { name: 'Linked' })).toContainElement(screen.getByTestId('own'))
  })

  it("says it couldn't copy when there is no clipboard", async () => {
    const user = userEvent.setup()
    setClipboard(undefined)
    render(<CopyButton value="ak_1" label="Copy key" />)
    await user.click(screen.getByRole('button', { name: 'Copy key' }))
    expect(await screen.findByRole('button', { name: "Couldn't copy" })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Copied' })).not.toBeInTheDocument()
  })

  it("says it couldn't copy when the write is rejected, and resets after a moment", async () => {
    const user = userEvent.setup()
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error('denied')) })
    render(<CopyButton value="ak_1" label="Copy key" />)
    await user.click(screen.getByRole('button', { name: 'Copy key' }))
    expect(await screen.findByRole('button', { name: "Couldn't copy" })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Copy key' }, { timeout: 3000 })).toBeInTheDocument()
  })

  it('clears its reset timer on unmount', async () => {
    const user = userEvent.setup()
    setClipboard({ writeText: vi.fn().mockResolvedValue(undefined) })
    const spy = vi.spyOn(globalThis, 'clearTimeout')
    const { unmount } = render(<CopyButton value="x" label="Copy" />)
    await user.click(screen.getByRole('button', { name: 'Copy' }))
    await screen.findByRole('button', { name: 'Copied' })
    spy.mockClear()
    unmount()
    expect(spy).toHaveBeenCalled()
    spy.mockRestore()
  })
})

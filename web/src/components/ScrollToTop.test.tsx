import { act, render } from '@testing-library/react'
import { MemoryRouter, useNavigate, type NavigateFunction } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ScrollToTop from './ScrollToTop'

let navigate: NavigateFunction
function Grab() {
  navigate = useNavigate()
  return null
}

describe('ScrollToTop', () => {
  const scrollTo = vi.fn()
  beforeEach(() => {
    vi.stubGlobal('scrollTo', scrollTo)
    render(
      <MemoryRouter initialEntries={['/projects']}>
        <ScrollToTop />
        <Grab />
      </MemoryRouter>,
    )
    scrollTo.mockClear()
  })
  afterEach(() => vi.unstubAllGlobals())

  it('scrolls to the top on a new path', () => {
    act(() => navigate('/inbox'))
    expect(scrollTo).toHaveBeenCalledWith(0, 0)
  })

  it('stays put on a query change', () => {
    act(() => navigate('/projects?range=7d'))
    expect(scrollTo).not.toHaveBeenCalled()
  })

  it('leaves back and forward to the browser', () => {
    act(() => navigate('/inbox'))
    scrollTo.mockClear()
    act(() => navigate(-1))
    expect(scrollTo).not.toHaveBeenCalled()
  })
})

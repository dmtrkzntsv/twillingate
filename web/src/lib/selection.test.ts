import { describe, expect, it } from 'vitest'
import { chooseSelection } from './selection'

const BOTH = { project: true, range: true }

describe('chooseSelection', () => {
  it('the URL wins over stored and defaults', () => {
    const url = new URLSearchParams('project=2&range=30d')
    const stored = { project_id: 5, range: '7d' }
    expect(chooseSelection(url, stored, [1, 2, 5], BOTH)).toEqual({ projectId: 2, range: '30d' })
  })

  it('the URL wins for a custom range with from/to', () => {
    const url = new URLSearchParams('range=custom&from=2026-01-01&to=2026-01-31')
    expect(chooseSelection(url, {}, [1], BOTH)).toEqual({
      projectId: 1,
      range: 'custom',
      from: '2026-01-01',
      to: '2026-01-31',
    })
  })

  it('falls back to the stored selection when the URL has none', () => {
    const url = new URLSearchParams()
    const stored = { project_id: 5, range: '30d' }
    expect(chooseSelection(url, stored, [1, 5], BOTH)).toEqual({ projectId: 5, range: '30d' })
  })

  it('falls back to the first active project when the stored one is stale', () => {
    const url = new URLSearchParams()
    const stored = { project_id: 99, range: '30d' }
    expect(chooseSelection(url, stored, [1, 2], BOTH)).toEqual({ projectId: 1, range: '30d' })
  })

  it('falls back to the first active project and 7d with nothing else given', () => {
    const url = new URLSearchParams()
    expect(chooseSelection(url, {}, [3, 4], BOTH)).toEqual({ projectId: 3, range: '7d' })
  })

  it('omits a part the dashboard has no switcher for', () => {
    const url = new URLSearchParams('project=1&range=30d')
    expect(chooseSelection(url, {}, [1], { project: false, range: false })).toEqual({})
  })

  it('omits only the project when there is no project switcher', () => {
    const url = new URLSearchParams()
    expect(chooseSelection(url, {}, [1], { project: false, range: true })).toEqual({ range: '7d' })
  })

  it('a custom range needs both from and to, else it falls through', () => {
    const url = new URLSearchParams('range=custom&from=2026-01-01')
    expect(chooseSelection(url, {}, [1], BOTH)).toEqual({ projectId: 1, range: '7d' })
  })

  it('an unknown preset falls through like a missing one', () => {
    const url = new URLSearchParams('range=decade')
    expect(chooseSelection(url, {}, [1], BOTH)).toEqual({ projectId: 1, range: '7d' })
  })

  it('a stale URL project falls back through stored to the first active one', () => {
    const url = new URLSearchParams('project=99')
    const stored = { project_id: 2 }
    expect(chooseSelection(url, stored, [1, 2], { project: true, range: false })).toEqual({
      projectId: 2,
    })
  })

  it('no active projects leaves projectId unset', () => {
    const url = new URLSearchParams()
    expect(chooseSelection(url, {}, [], { project: true, range: false })).toEqual({})
  })

  it('a malformed custom date falls through', () => {
    const url = new URLSearchParams('range=custom&from=01-01-2026&to=2026-01-31')
    expect(chooseSelection(url, {}, [1], BOTH)).toEqual({ projectId: 1, range: '7d' })
  })

  it('a custom range with from after to falls through', () => {
    const url = new URLSearchParams('range=custom&from=2026-02-01&to=2026-01-01')
    expect(chooseSelection(url, {}, [1], BOTH)).toEqual({ projectId: 1, range: '7d' })
  })

  it('a custom range over 365 days falls through', () => {
    const url = new URLSearchParams('range=custom&from=2025-01-01&to=2026-01-02')
    expect(chooseSelection(url, {}, [1], BOTH)).toEqual({ projectId: 1, range: '7d' })
  })

  it('a custom range of exactly 365 days is accepted', () => {
    const url = new URLSearchParams('range=custom&from=2025-01-01&to=2026-01-01')
    expect(chooseSelection(url, {}, [1], BOTH)).toEqual({
      projectId: 1,
      range: 'custom',
      from: '2025-01-01',
      to: '2026-01-01',
    })
  })

  it('an invalid URL custom range falls through to a valid stored one', () => {
    const url = new URLSearchParams('range=custom&from=bad&to=bad')
    const stored = { range: 'custom', from: '2026-01-01', to: '2026-01-05' }
    expect(chooseSelection(url, stored, [1], BOTH)).toEqual({
      projectId: 1,
      range: 'custom',
      from: '2026-01-01',
      to: '2026-01-05',
    })
  })
})

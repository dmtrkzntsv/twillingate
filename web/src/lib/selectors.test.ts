import { describe, expect, it } from 'vitest'
import type { Widget } from './api'
import { readSelection, selectionLabel, selectorProjects, widgetSelectors, writeSelection } from './selectors'

describe('readSelection and writeSelection', () => {
  it('round-trip values with commas, ampersands, equals, plus, spaces, unicode and the empty string', () => {
    const hostile = ['a,b', 'x&y', 'k=v', '1+1', 'two words', 'café ✓ 日本', '']
    const params = writeSelection(new URLSearchParams(), { deployment: hostile, campaign: ['spring'] })
    // Through the address bar and back, as text.
    const back = readSelection(new URLSearchParams(params.toString()), ['deployment', 'campaign'])
    expect(back).toEqual({ deployment: hostile, campaign: ['spring'] })
  })

  it('writes each value as its own s.<name> parameter', () => {
    const params = writeSelection(new URLSearchParams(), { campaign: ['a', 'b'] })
    expect(params.getAll('s.campaign')).toEqual(['a', 'b'])
  })

  it('drops s.<name> parameters the dashboard does not declare', () => {
    const params = new URLSearchParams('s.deployment=cloud&s.x=1&s.=2')
    expect(readSelection(params, ['deployment'])).toEqual({ deployment: ['cloud'] })
  })

  it('reads a name with no parameter as absent, which is All', () => {
    expect(readSelection(new URLSearchParams('project=7'), ['deployment'])).toEqual({})
  })

  it('leaves the other parameters alone and replaces the old selection', () => {
    const params = new URLSearchParams('project=7&range=custom&from=2026-09-01&to=2026-09-10&s.old=gone&s.deployment=cloud')
    const out = writeSelection(params, { deployment: ['self-hosted'] })
    expect(out.get('project')).toBe('7')
    expect(out.get('range')).toBe('custom')
    expect(out.get('from')).toBe('2026-09-01')
    expect(out.get('to')).toBe('2026-09-10')
    expect(out.getAll('s.deployment')).toEqual(['self-hosted'])
    expect(out.has('s.old')).toBe(false)
    // The parameters given are not changed in place.
    expect(params.getAll('s.deployment')).toEqual(['cloud'])
  })

  it('writes nothing for an empty list', () => {
    const out = writeSelection(new URLSearchParams('s.deployment=cloud'), { deployment: [] })
    expect(out.toString()).toBe('')
  })
})

function widget(extra: Partial<Widget>): Widget {
  return {
    widget_id: 1,
    dashboard_id: 1,
    name: 'w',
    component: 'stat',
    width: 3,
    height: 3,
    props: {},
    source: { type: 'sql', content: '…' },
    follows_project: true,
    follows_range: true,
    follows_selectors: [],
    ...extra,
  }
}

describe('widgetSelectors', () => {
  const order = ['deployment', 'campaign']

  it('keeps only the selectors the widget follows with values, in the dashboard order', () => {
    const w = widget({ follows_selectors: ['campaign', 'deployment'] })
    expect(widgetSelectors(w, { campaign: ['a'], deployment: ['cloud'], other: ['x'] }, order)).toEqual({
      deployment: ['cloud'],
      campaign: ['a'],
    })
    expect(Object.keys(widgetSelectors(w, { campaign: ['a'], deployment: ['cloud'] }, order)!)).toEqual(order)
  })

  it('is undefined when none the widget follows has values', () => {
    expect(widgetSelectors(widget({ follows_selectors: ['campaign'] }), { deployment: ['cloud'], campaign: [] }, order)).toBeUndefined()
    expect(widgetSelectors(widget({}), { deployment: ['cloud'] }, order)).toBeUndefined()
  })
})

describe('selectionLabel', () => {
  it('reads All, the one value, or how many', () => {
    expect(selectionLabel('Deployment', undefined)).toBe('Deployment: All')
    expect(selectionLabel('Deployment', [])).toBe('Deployment: All')
    expect(selectionLabel('Deployment', ['cloud'])).toBe('Deployment: cloud')
    expect(selectionLabel('Deployment', ['cloud', 'edge'])).toBe('Deployment: 2 selected')
  })
})

describe('selectorProjects', () => {
  const query = (project_id?: number) =>
    widget({ source: { type: 'query', content: JSON.stringify({ ...(project_id ? { project_id } : {}), query: { kind: 'stat' } }) } })
  const live = Array.from({ length: 12 }, (_, i) => i + 1)

  it('is the project shown, then each project a query widget fixes, once each', () => {
    expect(selectorProjects([query(4), query(9), query(4), widget({})], 7, live)).toEqual([7, 4, 9])
  })

  it('is the newest live projects, up to 10, when none is shown or fixed', () => {
    expect(selectorProjects([query(), widget({})], undefined, live)).toEqual([12, 11, 10, 9, 8, 7, 6, 5, 4, 3])
  })

  it('does not add a project_id of zero, which the backend treats as omitted', () => {
    const zero = widget({ source: { type: 'query', content: '{"project_id": 0, "query": {"kind": "stat"}}' } })
    expect(selectorProjects([zero], 7, live)).toEqual([7])
  })

  it('passes over a query it cannot read', () => {
    expect(selectorProjects([widget({ source: { type: 'query', content: '{' } })], 3, live)).toEqual([3])
  })
})

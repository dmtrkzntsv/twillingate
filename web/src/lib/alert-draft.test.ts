import { describe, expect, it } from 'vitest'
import { alert, brokenAlert } from '@/test/alerts'
import { definitionChanged, draftErrors, draftOf, patchOf, previewBody } from './alert-draft'

const rate = () =>
  alert({ unit: 'fraction', read: { metric: 'rate', volume: 'count' }, condition: { type: 'threshold', op: 'below', value: 0.03 }, window_days: 7, min_volume: 100 })

const template = () =>
  alert({
    template: 'event_count',
    fields: { event: 'signup' },
    query: { type: 'stat', event: 'signup' },
    read: { metric: 'count' },
    condition: { type: 'change', direction: 'drop', percent: 30, against: 'previous' },
  })

describe('draftOf', () => {
  it('writes the alert as the inputs show it, a fraction threshold as a percent', () => {
    const d = draftOf(rate())
    expect(d).toMatchObject({ name: 'Signups', conditionType: 'threshold', op: 'below', value: '3', windowDays: '7', minVolume: '100' })
  })

  it('gives the condition types the alert lacks their defaults', () => {
    const d = draftOf(rate())
    expect(d).toMatchObject({ direction: 'drop', percent: '30', against: 'previous' })
  })
})

describe('patchOf', () => {
  it('is empty with no edits', () => {
    expect(patchOf(alert(), draftOf(alert()))).toEqual({})
    expect(patchOf(rate(), draftOf(rate()))).toEqual({})
    expect(patchOf(template(), draftOf(template()))).toEqual({})
  })

  it('sends the name alone when only it changed', () => {
    expect(patchOf(alert(), { ...draftOf(alert()), name: 'Sign-ups' })).toEqual({ name: 'Sign-ups' })
  })

  it("sends a fraction threshold's value back as a fraction", () => {
    expect(patchOf(rate(), { ...draftOf(rate()), value: '4' })).toEqual({ condition: { type: 'threshold', op: 'below', value: 0.04 } })
  })

  it('sends the whole change condition when the type switches', () => {
    const a = alert()
    expect(patchOf(a, { ...draftOf(a), conditionType: 'change', percent: '25' })).toEqual({
      condition: { type: 'change', direction: 'drop', percent: 25, against: 'previous' },
    })
  })

  it('leaves min_volume out for a stat threshold with no volume', () => {
    const a = alert()
    expect(patchOf(a, { ...draftOf(a), minVolume: '20' })).toEqual({})
    // A change condition takes it.
    expect(patchOf(a, { ...draftOf(a), conditionType: 'change', minVolume: '20' })).toMatchObject({ min_volume: 20 })
  })

  it('clears a kept minimum volume the new condition does not take', () => {
    const a = alert({ condition: { type: 'change', direction: 'drop', percent: 30, against: 'previous' }, min_volume: 50 })
    expect(patchOf(a, { ...draftOf(a), conditionType: 'threshold', value: '10' })).toEqual({
      condition: { type: 'threshold', op: 'below', value: 10 },
      min_volume: 0,
    })
  })

  it('sends a changed window', () => {
    expect(patchOf(rate(), { ...draftOf(rate()), windowDays: '14' })).toEqual({ window_days: 14 })
  })

  it('sends the fields whole, and nothing else, when a template field changed', () => {
    const a = alert({ template: 'conversion', kind: 'funnel', fields: { from_event: 'visit', to_event: 'signup', people: 'user' } })
    const d = draftOf(a)
    expect(patchOf(a, { ...d, fields: { ...d.fields, to_event: 'paid' } })).toEqual({ fields: { from_event: 'visit', to_event: 'paid', people: 'user' } })
  })

  it('counts an empty field as an absent one', () => {
    const a = alert({ template: 'unique_people', fields: { who: 'users' } })
    const d = draftOf(a)
    expect(patchOf(a, { ...d, fields: { ...d.fields, event: '' } })).toEqual({})
    expect(patchOf(a, { ...d, fields: { event: 'signup', who: 'users' } })).toEqual({ fields: { event: 'signup', who: 'users' } })
    expect(patchOf(a, { ...d, fields: { event: '', who: '' } })).toEqual({ fields: {} })
  })

  it("sends a silence alert's hours, without a window or a minimum volume", () => {
    const a = alert({ kind: 'silence', unit: 'hours', condition: { type: 'silence', hours: 6 }, window_days: 1 })
    expect(patchOf(a, { ...draftOf(a), hours: '12', windowDays: '3', minVolume: '5' })).toEqual({ condition: { type: 'silence', hours: 12 } })
  })

  it("sends only a broken alert's name", () => {
    const a = brokenAlert()
    expect(patchOf(a, draftOf(a))).toEqual({})
    expect(patchOf(a, { ...draftOf(a), name: 'Older funnel' })).toEqual({ name: 'Older funnel' })
  })
})

describe('definitionChanged', () => {
  it('is false for a rename and true for a condition, window, volume or field change', () => {
    const a = rate()
    expect(definitionChanged(a, { ...draftOf(a), name: 'x' })).toBe(false)
    expect(definitionChanged(a, { ...draftOf(a), value: '5' })).toBe(true)
    expect(definitionChanged(a, { ...draftOf(a), windowDays: '3' })).toBe(true)
    expect(definitionChanged(a, { ...draftOf(a), minVolume: '10' })).toBe(true)
    const t = template()
    expect(definitionChanged(t, { ...draftOf(t), fields: { event: 'paid' } })).toBe(true)
  })
})

describe('previewBody', () => {
  it("carries a template alert's template with the edited fields, and no query", () => {
    const a = template()
    const body = previewBody(a, { ...draftOf(a), fields: { event: 'paid' }, windowDays: '7' })
    expect(body).toEqual({
      template: 'event_count',
      fields: { event: 'paid' },
      condition: { type: 'change', direction: 'drop', percent: 30, against: 'previous' },
      window_days: 7,
      min_volume: 0,
    })
    expect(body).not.toHaveProperty('query')
  })

  it("carries a query alert's query and read, and min_volume only where taken", () => {
    const a = alert()
    expect(previewBody(a, { ...draftOf(a), value: '40' })).toEqual({
      query: a.query,
      read: a.read,
      condition: { type: 'threshold', op: 'below', value: 40 },
      window_days: 1,
    })
  })

  it("carries a silence alert's where and hours", () => {
    const where = [{ field: 'event', op: 'eq', value: 'signup' }]
    const a = alert({ kind: 'silence', unit: 'hours', where, condition: { type: 'silence', hours: 6 } })
    expect(previewBody(a, { ...draftOf(a), hours: '8' })).toEqual({ where, condition: { type: 'silence', hours: 8 } })
  })
})

describe('draftErrors', () => {
  it('has none for a saved alert', () => {
    expect(draftErrors(alert(), draftOf(alert()))).toEqual({})
    expect(draftErrors(rate(), draftOf(rate()))).toEqual({})
  })

  it('refuses an empty name and a threshold that is not a number', () => {
    const a = alert()
    expect(draftErrors(a, { ...draftOf(a), name: '  ' })).toHaveProperty('name')
    expect(draftErrors(a, { ...draftOf(a), value: 'abc' })).toHaveProperty('value')
    expect(draftErrors(a, { ...draftOf(a), value: '' })).toHaveProperty('value')
  })

  it('refuses a percent of 0 or over 1000', () => {
    const a = alert({ condition: { type: 'change', direction: 'drop', percent: 30, against: 'previous' } })
    expect(draftErrors(a, { ...draftOf(a), percent: '0' })).toHaveProperty('percent')
    expect(draftErrors(a, { ...draftOf(a), percent: '1001' })).toHaveProperty('percent')
    expect(draftErrors(a, { ...draftOf(a), percent: '1000' })).toEqual({})
  })

  it('refuses hours past 720, or not whole', () => {
    const a = alert({ kind: 'silence', unit: 'hours', condition: { type: 'silence', hours: 6 }, window_days: 0 })
    expect(draftErrors(a, { ...draftOf(a), hours: '721' })).toHaveProperty('hours')
    expect(draftErrors(a, { ...draftOf(a), hours: '1.5' })).toHaveProperty('hours')
    // No window to judge for silence, even a stored 0.
    expect(draftErrors(a, draftOf(a))).toEqual({})
  })

  it('refuses a window of 31 days', () => {
    expect(draftErrors(alert(), { ...draftOf(alert()), windowDays: '31' })).toHaveProperty('windowDays')
    expect(draftErrors(alert(), { ...draftOf(alert()), windowDays: '0' })).toHaveProperty('windowDays')
  })

  it('refuses a minimum volume between 0 and 1, where one is taken', () => {
    expect(draftErrors(rate(), { ...draftOf(rate()), minVolume: '0.5' })).toHaveProperty('minVolume')
    expect(draftErrors(rate(), { ...draftOf(rate()), minVolume: '0' })).toEqual({})
    // A stat threshold without volume does not take one.
    expect(draftErrors(alert(), { ...draftOf(alert()), minVolume: '0.5' })).toEqual({})
  })

  it("refuses a template's missing event", () => {
    const a = template()
    expect(draftErrors(a, { ...draftOf(a), fields: { event: '' } })).toHaveProperty('fields')
    const u = alert({ template: 'unique_people', fields: {} })
    expect(draftErrors(u, draftOf(u))).toEqual({})
  })

  it("judges only a broken alert's name", () => {
    const a = brokenAlert()
    expect(draftErrors(a, draftOf(a))).toEqual({})
    expect(draftErrors(a, { ...draftOf(a), name: '' })).toHaveProperty('name')
  })
})

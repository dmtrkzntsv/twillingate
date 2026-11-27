import { describe, expect, it } from 'vitest'
import { blankAlert, blankDraft, createBody, stepTwoErrors, suggestName, TEMPLATES, wizardAlert } from './alert-wizard'

function drafted(template: Parameters<typeof blankAlert>[1], over: Record<string, unknown> = {}, fields: Record<string, string> = {}) {
  const blank = blankAlert(1, template)
  return { blank, draft: { ...blankDraft(blank), ...over, fields } as ReturnType<typeof blankDraft> }
}

describe('TEMPLATES', () => {
  it('lists the four in order', () => {
    expect(TEMPLATES.map((t) => t.id)).toEqual(['event_count', 'unique_people', 'conversion', 'no_events'])
  })
})

describe('blankAlert', () => {
  it.each([
    ['event_count', 'stat', 'count', { metric: 'count' }, 1, 'threshold'],
    ['unique_people', 'stat', 'count', { metric: 'actors' }, 1, 'threshold'],
    ['conversion', 'funnel', 'fraction', { step: '2', from: 'start' }, 7, 'threshold'],
    ['no_events', 'silence', 'hours', {}, 1, 'silence'],
  ] as const)('%s', (template, kind, unit, read, days, type) => {
    const a = blankAlert(4, template)
    expect(a).toMatchObject({ id: 0, project_id: 4, template, fields: {}, kind, unit, read, window_days: days, min_volume: 0, recent: [] })
    expect(a.condition.type).toBe(type)
  })

  it('makes a usable draft', () => {
    expect(blankDraft(blankAlert(1, 'event_count'))).toMatchObject({ conditionType: 'threshold', op: 'below', value: '', windowDays: '1' })
    expect(blankDraft(blankAlert(1, 'no_events'))).toMatchObject({ conditionType: 'silence', hours: '6' })
  })
})

describe('wizardAlert', () => {
  it('follows who in the read', () => {
    const { blank, draft } = drafted('unique_people', {}, { who: 'users' })
    expect(wizardAlert(blank, draft).read.metric).toBe('users')
    expect(wizardAlert(blank, { ...draft, fields: {} }).read.metric).toBe('actors')
  })
})

describe('suggestName', () => {
  it('names each template', () => {
    let d = drafted('event_count', { value: '5' }, { event: 'signup' })
    expect(suggestName(d.blank, d.draft)).toBe('Count of signup below 5 over 1 day')
    d = drafted('conversion', { value: '3', windowDays: '7' }, { from_event: 'signup_start', to_event: 'signup_done' })
    expect(suggestName(d.blank, d.draft)).toBe('Conversion from signup_start to signup_done below 3% over 7 days')
    d = drafted('no_events', {}, { event: 'heartbeat' })
    expect(suggestName(d.blank, d.draft)).toBe('No heartbeat for 6 hours')
    d = drafted('no_events', { hours: '1' }, { event: 'heartbeat', path: '/x' })
    expect(suggestName(d.blank, d.draft)).toBe('No heartbeat on /x for 1 hour')
  })

  it('names an above threshold', () => {
    const d = drafted('unique_people', { op: 'above', value: '100' }, { event: 'signup' })
    expect(suggestName(d.blank, d.draft)).toBe('Visitors doing signup above 100 over 1 day')
  })

  it('caps at 100 characters at a word boundary', () => {
    const event = 'a'.repeat(60) + ' ' + 'b'.repeat(60)
    const d = drafted('event_count', { value: '5' }, { event })
    const name = suggestName(d.blank, d.draft)
    expect(name.length).toBeLessThanOrEqual(100)
    expect(name).toBe('Count of ' + 'a'.repeat(60))
  })

  it('cuts at the last word boundary even before a long word', () => {
    const d = drafted('event_count', { value: '5' }, { event: 'x'.repeat(200) })
    expect(suggestName(d.blank, d.draft)).toBe('Count of')
  })
})

describe('createBody', () => {
  it('sends a conversion threshold in the unit of the read, with its window', () => {
    const { blank, draft } = drafted('conversion', { value: '3', name: ' Signup funnel ' }, { from_event: 'a', to_event: 'b', people: 'actor' })
    expect(createBody(blank, draft)).toEqual({
      name: 'Signup funnel',
      template: 'conversion',
      fields: { from_event: 'a', to_event: 'b' },
      condition: { type: 'threshold', op: 'below', value: 0.03 },
      window_days: 7,
      min_volume: 0,
    })
  })

  it('sends no window and no minimum volume for no_events', () => {
    const { blank, draft } = drafted('no_events', { name: 'n' }, { event: 'hb' })
    const body = createBody(blank, draft)
    expect(body).toEqual({ name: 'n', template: 'no_events', fields: { event: 'hb' }, condition: { type: 'silence', hours: 6 } })
    expect(body).not.toHaveProperty('window_days')
    expect(body).not.toHaveProperty('min_volume')
  })

  it('sends no minimum volume for an event count threshold', () => {
    const { blank, draft } = drafted('event_count', { value: '5', name: 'n', minVolume: '10' }, { event: 'signup', path: '' })
    const body = createBody(blank, draft)
    expect(body).toEqual({ name: 'n', template: 'event_count', fields: { event: 'signup' }, condition: { type: 'threshold', op: 'below', value: 5 }, window_days: 1 })
  })

  it('sends the minimum volume of a change', () => {
    const { blank, draft } = drafted('event_count', { conditionType: 'change', percent: '30', minVolume: '10', name: 'n' }, { event: 'signup' })
    expect(createBody(blank, draft)).toMatchObject({
      condition: { type: 'change', direction: 'drop', percent: 30, against: 'previous' },
      window_days: 1,
      min_volume: 10,
    })
  })

  it('drops who: actors but keeps who: users', () => {
    let d = drafted('unique_people', { value: '5', name: 'n' }, { who: 'actors', event: 'signup' })
    expect(createBody(d.blank, d.draft)).toMatchObject({ fields: { event: 'signup' } })
    d = drafted('unique_people', { value: '5', name: 'n' }, { who: 'users' })
    expect(createBody(d.blank, d.draft)).toMatchObject({ fields: { who: 'users' } })
  })
})

describe('stepTwoErrors', () => {
  it('ignores the name and flags missing events and an empty threshold', () => {
    const { blank, draft } = drafted('event_count')
    expect(Object.keys(stepTwoErrors(blank, draft)).sort()).toEqual(['fields', 'value'])
    const ok = drafted('event_count', { value: '5' }, { event: 'signup' })
    expect(stepTwoErrors(ok.blank, ok.draft)).toEqual({})
  })
})

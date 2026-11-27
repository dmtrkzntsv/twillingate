import { describe, expect, it } from 'vitest'
import type { Alert, AlertCondition } from './api'
import {
  alertSource,
  breachedSince,
  checkBlocked,
  conditionTypes,
  definitionKey,
  describeCondition,
  describeSource,
  formatAlertValue,
  inputToThreshold,
  warningsText,
  warningText,
  snoozeUntil,
  stateLabel,
  takesMinVolume,
  thresholdToInput,
} from './alerts'

const below: AlertCondition = { type: 'threshold', op: 'below', value: 0.03 }

function alert(over: Partial<Alert> = {}): Alert {
  return {
    id: 1,
    project_id: 3,
    name: 'a',
    kind: 'stat',
    unit: 'count',
    query: { type: 'stat' },
    read: { metric: 'count' },
    condition: below,
    window_days: 1,
    min_volume: 0,
    paused: false,
    snoozed_until: '',
    state: 'ok',
    reason: '',
    last_value: null,
    last_value_text: '—',
    last_evaluated_at: '',
    last_day: '',
    check_requested_at: '',
    check_pending: false,
    recent: [],
    created_at: '',
    updated_at: '',
    archived_at: '',
    ...over,
  }
}

describe('formatAlertValue', () => {
  it('formats by unit', () => {
    expect(formatAlertValue('count', 1240)).toBe('1,240')
    expect(formatAlertValue('fraction', 0.034)).toBe('3.4%')
    expect(formatAlertValue('number', 2.5)).toBe('2.5')
    expect(formatAlertValue('number', 2.456)).toBe('2.46')
    expect(formatAlertValue('hours', 7.9)).toBe('7h')
    expect(formatAlertValue('hours', 0.75)).toBe('45m')
  })
  it('says a dash for nothing', () => {
    for (const u of ['count', 'fraction', 'number', 'hours', ''] as const) expect(formatAlertValue(u, null)).toBe('—')
  })
})

describe('threshold input', () => {
  it('shows a fraction as a percent without float noise', () => {
    expect(thresholdToInput('fraction', 0.07)).toBe('7')
    expect(thresholdToInput('count', 5)).toBe('5')
  })
  it('reads the input back in the unit', () => {
    expect(inputToThreshold('fraction', '3')).toBe(0.03)
    expect(inputToThreshold('count', '12')).toBe(12)
    expect(inputToThreshold('count', '')).toBeNull()
    expect(inputToThreshold('count', 'abc')).toBeNull()
    expect(inputToThreshold('count', 'Infinity')).toBeNull()
  })
  it('round-trips a saved value unchanged', () => {
    for (const v of [0.035, 0.07, 0.03, 0.0001, 0.123]) {
      expect(inputToThreshold('fraction', thresholdToInput('fraction', v))).toBe(v)
    }
  })
})

describe('describeCondition', () => {
  it('describes thresholds in the read unit', () => {
    expect(describeCondition({ condition: below, unit: 'fraction', window_days: 7 })).toBe('below 3% over 7 days')
    expect(
      describeCondition({ condition: { type: 'threshold', op: 'above', value: 1200 }, unit: 'count', window_days: 1 }),
    ).toBe('above 1,200 over 1 day')
    expect(
      describeCondition({ condition: { type: 'threshold', op: 'below', value: 0.035 }, unit: 'fraction', window_days: 1 }),
    ).toBe('below 3.5% over 1 day')
    expect(
      describeCondition({ condition: { type: 'threshold', op: 'above', value: 2.456 }, unit: 'number', window_days: 2 }),
    ).toBe('above 2.46 over 2 days')
    expect(
      describeCondition({ condition: { type: 'threshold', op: 'below', value: 0.5 }, unit: 'count', window_days: 1 }),
    ).toBe('below 0.5 over 1 day')
  })
  it('describes changes', () => {
    const c = (direction: 'drop' | 'rise' | 'either', against: 'previous' | 'week_ago'): AlertCondition => ({
      type: 'change',
      direction,
      percent: 30,
      against,
    })
    expect(describeCondition({ condition: c('drop', 'previous'), unit: 'count', window_days: 7 })).toBe(
      'drops 30% against the 7 days before',
    )
    expect(describeCondition({ condition: c('rise', 'week_ago'), unit: 'count', window_days: 1 })).toBe(
      'rises 30% against the same day a week earlier',
    )
    expect(describeCondition({ condition: c('drop', 'previous'), unit: 'count', window_days: 1 })).toBe(
      'drops 30% against the day before',
    )
    expect(describeCondition({ condition: c('either', 'week_ago'), unit: 'count', window_days: 7 })).toBe(
      'moves 30% against the same 7 days a week earlier',
    )
  })
  it('describes silence', () => {
    expect(describeCondition({ condition: { type: 'silence', hours: 6 }, unit: 'hours', window_days: 1 })).toBe(
      'no event for 6 hours',
    )
    expect(describeCondition({ condition: { type: 'silence', hours: 1 }, unit: 'hours', window_days: 1 })).toBe(
      'no event for 1 hour',
    )
  })
  it('says nothing for a broken alert, whose condition has no type the console knows', () => {
    const broken = { type: '' } as unknown as AlertCondition
    expect(describeCondition({ condition: broken, unit: '', window_days: 0 })).toBe('')
    const unknown = { type: 'forecast' } as unknown as AlertCondition
    expect(describeCondition({ condition: unknown, unit: 'count', window_days: 7 })).toBe('')
  })
})

describe('describeSource', () => {
  const tpl = (template: Alert['template'], fields: Record<string, string>, over: Partial<Alert> = {}) =>
    describeSource(alert({ template, fields, ...over }))
  it('describes templates', () => {
    expect(tpl('event_count', { event: 'signup' })).toBe('Count of signup')
    expect(tpl('event_count', { event: '$page_view', path: '/pricing' })).toBe('Count of $page_view on /pricing')
    expect(tpl('unique_people', { event: 'signup' })).toBe('Visitors doing signup')
    expect(tpl('unique_people', { event: 'signup', who: 'users' })).toBe('Signed-in users doing signup')
    expect(tpl('unique_people', {})).toBe('Visitors')
    expect(tpl('conversion', { from_event: 'signup_start', to_event: 'signup_done' })).toBe(
      'Conversion from signup_start to signup_done',
    )
    expect(tpl('conversion', { from_event: 'a', to_event: 'b', people: 'user' })).toBe(
      'Conversion from a to b (signed-in users)',
    )
    expect(tpl('conversion', { from_event: '$page_view', from_path: '/a', to_event: 'signup', to_path: '/b' })).toBe(
      'Conversion from $page_view on /a to signup on /b',
    )
    expect(tpl('conversion', { from_event: '$form_submit', from_path: '/contact', to_event: 'paid' })).toBe(
      'Conversion from $form_submit on /contact to paid',
    )
    expect(tpl('no_events', { event: 'heartbeat' })).toBe('Events named heartbeat')
    expect(tpl('no_events', { event: '$page_view', path: '/x' })).toBe('Events named $page_view on /x')
  })
  it('describes the rest', () => {
    expect(describeSource(alert({ kind: 'funnel', read: { step: 'Paid' } }))).toBe('Funnel step Paid over the first step')
    expect(describeSource(alert({ kind: 'funnel', read: { step: 'Paid', from: 'previous' } }))).toBe(
      'Funnel step Paid over the step before',
    )
    expect(describeSource(alert({ read: { metric: 'bounce_rate' } }))).toBe('bounce_rate of a stat query')
    expect(describeSource(alert({ kind: 'silence', where: [{}, {}] }))).toBe('Events matching 2 filters')
    expect(describeSource(alert({ kind: 'silence', where: [{}] }))).toBe('Events matching 1 filter')
    expect(describeSource(alert({ kind: 'silence', where: [] }))).toBe('Every event')
    expect(describeSource(alert({ kind: 'silence' }))).toBe('Every event')
    expect(describeSource(alert({ kind: '', unit: '' }))).toBe('A definition that no longer holds')
    expect(describeSource(alert({ kind: '', unit: '', template: 'event_count', fields: { event: 'checkout' } }))).toBe('A definition that no longer holds')
  })
})

describe('alertSource', () => {
  it('sends the template alone', () => {
    expect(alertSource(alert({ template: 'event_count', fields: { event: 'x' }, query: { a: 1 } }))).toEqual({
      template: 'event_count',
      fields: { event: 'x' },
    })
  })
  it('sends where alone for a silence alert', () => {
    const where = [{ column: 'event_name', op: 'eq', values: ['x'] }]
    expect(alertSource(alert({ kind: 'silence', where, query: { a: 1 } }))).toEqual({ where })
  })
  it('sends query and read for a stat', () => {
    expect(alertSource(alert({ query: { a: 1 }, read: { metric: 'count' } }))).toEqual({
      query: { a: 1 },
      read: { metric: 'count' },
    })
  })
  it('sends an empty where for a silence alert of every event', () => {
    expect(alertSource(alert({ kind: 'silence', where: undefined }))).toEqual({ where: [] })
  })
})

describe('takesMinVolume', () => {
  const change: AlertCondition = { type: 'change', direction: 'drop', percent: 30, against: 'previous' }
  it('follows the server rule', () => {
    expect(takesMinVolume('stat', below, { metric: 'count' })).toBe(false)
    expect(takesMinVolume('stat', below, { metric: 'ratio', volume: 'count' })).toBe(true)
    expect(takesMinVolume('funnel', below, { step: 'x' })).toBe(true)
    expect(takesMinVolume('silence', { type: 'silence', hours: 6 }, {})).toBe(false)
    expect(takesMinVolume('stat', change, { metric: 'count' })).toBe(true)
    expect(takesMinVolume('', below, {})).toBe(false)
  })
})

describe('conditionTypes', () => {
  it('limits a kind to its conditions', () => {
    expect(conditionTypes('silence')).toEqual(['silence'])
    expect(conditionTypes('stat')).toEqual(['threshold', 'change'])
    expect(conditionTypes('funnel')).toEqual(['threshold', 'change'])
    expect(conditionTypes('')).toEqual([])
  })
})

describe('breachedSince', () => {
  const r = (at: string, outcome: Alert['recent'][number]['outcome']) => ({ at, value: 1, outcome })
  it('finds the start of the newest breached run, skipping no_data', () => {
    const recent = [r('t3', 'breached'), r('t2', 'no_data'), r('t1', 'breached'), r('t0', 'ok')]
    expect(breachedSince(alert({ state: 'breached', recent }))).toBe('t1')
  })
  it('stops at an ok evaluation; a failure or a broken one carries the breach on', () => {
    const recent = [r('t3', 'breached'), r('t2', 'ok'), r('t1', 'breached')]
    expect(breachedSince(alert({ state: 'breached', recent }))).toBe('t3')
    const carried = [r('t3', 'broken'), r('t2', 'error'), r('t1', 'breached'), r('t0', 'ok')]
    expect(breachedSince(alert({ state: 'breached', recent: carried }))).toBe('t1')
  })
  it('is null when the run fills a full recent with no ok before it, since the breach began earlier', () => {
    const full = Array.from({ length: 14 }, (_, i) => r(`t${13 - i}`, i % 3 === 1 ? 'no_data' : 'breached'))
    expect(breachedSince(alert({ state: 'breached', recent: full }))).toBeNull()
    // A short recent is every evaluation there is: the run's oldest is when it began.
    expect(breachedSince(alert({ state: 'breached', recent: full.slice(0, 13) }))).toBe('t1')
    // An ok as the 14th ends the run inside recent.
    expect(breachedSince(alert({ state: 'breached', recent: [...full.slice(0, 13), r('t0', 'ok')] }))).toBe('t1')
  })
  it('is null unless breached', () => {
    expect(breachedSince(alert({ state: 'ok', recent: [r('t1', 'breached')] }))).toBeNull()
    expect(breachedSince(alert({ state: 'breached', recent: [] }))).toBeNull()
  })
})

describe('snoozeUntil', () => {
  it('adds the choice to now, in UTC', () => {
    const now = new Date('2026-11-26T10:00:00.123Z')
    expect(snoozeUntil(now, '1h')).toBe('2026-11-26T11:00:00Z')
    expect(snoozeUntil(now, '1d')).toBe('2026-11-27T10:00:00Z')
    expect(snoozeUntil(now, '1w')).toBe('2026-12-03T10:00:00Z')
  })
})

describe('stateLabel', () => {
  it('names each state as the badge does', () => {
    expect(stateLabel('pending')).toBe('Pending')
    expect(stateLabel('ok')).toBe('OK')
    expect(stateLabel('breached')).toBe('Breached')
    expect(stateLabel('no_data')).toBe('No data')
    expect(stateLabel('broken')).toBe('Broken')
    expect(stateLabel('paused')).toBe('Paused')
    expect(stateLabel('snoozed')).toBe('Snoozed')
  })
  it('names a state it does not know as it is sent', () => {
    expect(stateLabel('muted' as Alert['state'])).toBe('muted')
  })
})

describe('definitionKey', () => {
  it('is the same across a rename, a pause, a snooze or a new updated_at, and differs for any part of the definition', () => {
    const a = alert({ template: 'event_count', fields: { event: 'signup' } })
    const k = definitionKey(a)
    expect(definitionKey({ ...a, name: 'x', paused: true, state: 'paused', snoozed_until: '2026-12-01T00:00:00Z', updated_at: '2026-11-26T09:00:00Z' })).toBe(k)
    const changes: Partial<Alert>[] = [
      { condition: { ...below, value: 0.04 } },
      { window_days: 14 },
      { min_volume: 10 },
      { fields: { event: 'paid' } },
      { template: undefined },
      { query: { type: 'stat', event: 'paid' } },
      { read: { metric: 'actors' } },
      { where: [] },
    ]
    for (const c of changes) expect(definitionKey({ ...a, ...c })).not.toBe(k)
  })
})

describe('checkBlocked', () => {
  it('says why an archived, paused or snoozed alert is not checked, archived first', () => {
    expect(checkBlocked(alert({ archived_at: '2026-11-20T10:00:00Z', paused: true }))).toBe('Archived alerts are not checked')
    expect(checkBlocked(alert({ paused: true, state: 'paused', snoozed_until: '2026-12-01T00:00:00Z' }))).toBe('Paused alerts are not checked')
    expect(checkBlocked(alert({ state: 'snoozed', snoozed_until: '2026-12-01T00:00:00Z' }))).toBe('Snoozed alerts are not checked')
    expect(checkBlocked(alert({ paused: true, state: 'paused' }), true)).toBe('Alerts of an archived project are not checked')
    expect(checkBlocked(alert({ archived_at: '2026-11-20T10:00:00Z' }), true)).toBe('Archived alerts are not checked')
    expect(checkBlocked(alert({ state: 'breached' }), false)).toBeNull()
  })
  it('is null for an alert that may be checked', () => {
    expect(checkBlocked(alert({ state: 'breached' }))).toBeNull()
  })
})

describe('warningText', () => {
  const never = (name: string) => `\`${name}\` has never been sent by this project; the alert reads no_data until it is`
  it('says an event the project never sent as Never sent yet, any other warning as the server words it', () => {
    expect(warningText(never('signup'))).toBe('Never sent yet: signup')
    expect(warningText('something else')).toBe('something else')
  })
  it('joins the events never sent into one line, the other warnings after it', () => {
    expect(warningsText([never('signup'), 'something else', never('paid')])).toEqual(['Never sent yet: signup, paid', 'something else'])
    expect(warningsText(['something else'])).toEqual(['something else'])
    expect(warningsText([])).toEqual([])
  })
})

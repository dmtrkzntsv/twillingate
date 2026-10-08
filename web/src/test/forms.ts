import type { FoundSubmission, Form, Submission, SubmissionsPage } from '@/lib/api'

const DAY = 86_400_000

/** An ISO time `days` from now (negative: in the past), without milliseconds, as the API writes it. */
export function fromNow(days: number): string {
  return new Date(Date.now() + days * DAY).toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/** A form as list_forms answers it: an approved one unless `over` says otherwise. */
export function form(name: string, over: Partial<Form> = {}): Form {
  return {
    name,
    status: 'approved',
    purpose: '',
    return_url: '',
    fields: ['email', 'message'],
    expected_fields: ['email', 'message'],
    created_at: fromNow(-10),
    approved_at: fromNow(-9),
    submissions: 0,
    archived: false,
    ...over,
  }
}

/** A draft whose window ends `days` from now. */
export function draft(name: string, days: number, over: Partial<Form> = {}): Form {
  return form(name, { status: 'draft', expected_fields: undefined, approved_at: undefined, draft_until: fromNow(days), ...over })
}

/** Two submissions of an approved contact form (expected email, message), newest first. */
export const contactPage: SubmissionsPage = {
  columns: ['Received', 'email', 'message'],
  rows: [
    ['2026-10-05T10:00:00Z', 'ann@example.com', 'Hello'],
    ['2026-10-04T09:00:00Z', 'bob@example.com', 'Hi there'],
  ],
  ids: ['s1', 's2'],
  matched: 2,
  total: 2,
  offset: 0,
  limit: 1000,
}

export function submission(id: string, over: Partial<Submission> = {}): Submission {
  return {
    id,
    form: 'contact',
    received_at: '2026-10-04T09:00:00Z',
    fields: { email: 'bob@example.com', message: 'Hi there', phone: '555-0100' },
    attribution: { referrer: 'google.com', utm_source: 'news', utm_medium: 'email', utm_campaign: 'fall' },
    ...over,
  }
}

/** A submission as find_submissions answers it: of an active form unless `over` says otherwise. */
export function found(id: string, over: Partial<FoundSubmission> = {}): FoundSubmission {
  return { ...submission(id), archived: false, ...over }
}

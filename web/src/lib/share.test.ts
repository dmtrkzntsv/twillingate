import { describe, expect, it } from 'vitest'
import type { WidgetShare } from './api'
import { archiveLabel, embedCode, rangeInWords, shareCaption, type ShareContext } from './share'

function share(over: Partial<WidgetShare> = {}): WidgetShare {
  return {
    id: 'abc',
    url: 'https://t.example/share/abc',
    image_url: 'https://t.example/share/abc.png',
    image_2x_url: 'https://t.example/share/abc@2x.png',
    widget_id: 1,
    dashboard_id: 2,
    dashboard_title: 'Overview',
    project_id: 3,
    project_name: 'Site',
    from: '2026-09-05',
    to: '2026-10-04',
    title: 'Views',
    created_at: '2026-10-05T10:00:00Z',
    archive_at: '2026-11-04T10:00:00Z',
    archived_at: null,
    caption_project: true,
    caption_range: true,
    ...over,
  }
}

describe('rangeInWords', () => {
  it.each([
    ['2026-09-05', '2026-10-04', 'Sep 5 – Oct 4, 2026'],
    ['2025-12-20', '2026-01-10', 'Dec 20, 2025 – Jan 10, 2026'],
    ['2026-09-05', '2026-09-05', 'Sep 5, 2026'],
  ])('%s to %s reads %s', (from, to, want) => {
    expect(rangeInWords(from, to)).toBe(want)
  })

  it('writes a value that is not a date as it is', () => {
    expect(rangeInWords('last week', '2026-10-04')).toBe('last week – 2026-10-04')
  })
})

describe('archiveLabel', () => {
  it('names the project lifetime when there is no archive date', () => {
    expect(archiveLabel(share({ archive_at: null }))).toBe('Project lifetime')
  })

  const now = new Date('2026-10-06T12:00:00Z')

  it('writes the archive date in UTC', () => {
    expect(archiveLabel(share({ archive_at: '2026-11-04T10:00:00Z' }), now)).toBe('Nov 4')
    expect(archiveLabel(share({ archive_at: '2026-11-04T23:30:00Z' }), now)).toBe('Nov 4')
  })

  it('adds the year when it is not this year', () => {
    expect(archiveLabel(share({ archive_at: '2027-01-04T10:00:00Z' }), now)).toBe('Jan 4, 2027')
    expect(archiveLabel(share({ archive_at: '2027-10-06T10:00:00Z' }), now)).toBe('Oct 6, 2027')
  })
})

describe('embedCode', () => {
  it('links the image, with the 2x one as srcset', () => {
    expect(embedCode(share())).toBe(
      '<a href="https://t.example/share/abc"><img src="https://t.example/share/abc.png" srcset="https://t.example/share/abc@2x.png 2x" alt="Views" width="600" height="315"></a>'
    )
  })

  it('escapes quotes, angle brackets and ampersands in the title', () => {
    const code = embedCode(share({ title: '<b>"q" & co' }))
    expect(code).toContain('alt="&lt;b&gt;&quot;q&quot; &amp; co"')
    expect(code).not.toContain('<b>')
  })
})

describe('shareCaption', () => {
  const ctx: ShareContext = { project: { id: 7, name: 'blog' }, from: '2026-09-05', to: '2026-10-04', rangeShown: true, writable: true }
  const follows = (follows_project: boolean, follows_range: boolean) => ({ follows_project, follows_range })

  it('names the project and the range the widget follows', () => {
    expect(shareCaption(follows(true, true), ctx)).toEqual({ projectName: 'blog', from: '2026-09-05', to: '2026-10-04' })
  })

  it('leaves out what the widget does not follow', () => {
    expect(shareCaption(follows(false, true), ctx)).toEqual({ from: '2026-09-05', to: '2026-10-04' })
    expect(shareCaption(follows(true, false), ctx)).toEqual({ projectName: 'blog' })
    expect(shareCaption(follows(false, false), ctx)).toEqual({})
  })

  it('leaves out what the page does not hand down: no project, no range switcher', () => {
    expect(shareCaption(follows(true, true), { ...ctx, project: undefined })).toEqual({ from: '2026-09-05', to: '2026-10-04' })
    expect(shareCaption(follows(true, true), { ...ctx, rangeShown: false })).toEqual({ projectName: 'blog' })
  })
})

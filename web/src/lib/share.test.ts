import { describe, expect, it } from 'vitest'
import type { WidgetShare } from './api'
import { archiveLabel, embedCode, rangeInWords } from './share'

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

  it('writes the archive date in UTC', () => {
    expect(archiveLabel(share({ archive_at: '2026-11-04T10:00:00Z' }))).toBe('Nov 4')
    expect(archiveLabel(share({ archive_at: '2026-11-04T23:30:00Z' }))).toBe('Nov 4')
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

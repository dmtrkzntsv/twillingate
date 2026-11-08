import { afterEach, describe, expect, it, vi } from 'vitest'
import { LAST_FORM_KEY, readLastForm, writeLastForm } from './last-form'

afterEach(() => {
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('last form', () => {
  it('remembers one form per project', () => {
    writeLastForm(1, 'contact')
    writeLastForm(2, 'signup')
    writeLastForm(1, 'survey')
    expect(readLastForm(1)).toBe('survey')
    expect(readLastForm(2)).toBe('signup')
    expect(readLastForm(3)).toBeNull()
  })

  it('ignores junk', () => {
    for (const junk of ['"x"', '["a"]', '{"1":10}', '{"1":""}', 'not json', 'null']) {
      localStorage.setItem(LAST_FORM_KEY, junk)
      expect(readLastForm(1)).toBeNull()
    }
  })

  it('survives blocked storage', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    expect(readLastForm(1)).toBeNull()
    expect(() => writeLastForm(1, 'contact')).not.toThrow()
  })
})

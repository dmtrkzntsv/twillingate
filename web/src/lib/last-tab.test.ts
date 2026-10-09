import { afterEach, describe, expect, it, vi } from 'vitest'
import { LAST_TAB_KEY, readLastTab, writeLastTab } from './last-tab'

afterEach(() => {
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('last tab', () => {
  it('remembers one dashboard per project', () => {
    writeLastTab(1, 10)
    writeLastTab(2, 20)
    writeLastTab(1, 11)
    expect(readLastTab(1)).toBe(11)
    expect(readLastTab(2)).toBe(20)
    expect(readLastTab(3)).toBeNull()
  })

  it('ignores junk', () => {
    for (const junk of ['"x"', '[1]', '{"1":"10"}', 'not json', 'null']) {
      localStorage.setItem(LAST_TAB_KEY, junk)
      expect(readLastTab(1)).toBeNull()
    }
  })

  it('survives blocked storage', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    expect(() => writeLastTab(1, 10)).not.toThrow()
    expect(readLastTab(1)).toBeNull()
  })
})

import { afterEach, expect, it } from 'vitest'
import { appVersion } from './version'

afterEach(() => {
  document.head.querySelector('meta[name="twillingate-version"]')?.remove()
})

function stamp(content: string) {
  const meta = document.createElement('meta')
  meta.name = 'twillingate-version'
  meta.content = content
  document.head.append(meta)
}

it('reads the version the server stamped into the shell', () => {
  stamp('v0.14.1')
  expect(appVersion()).toBe('v0.14.1')
})

it('is dev without the tag or with it empty', () => {
  expect(appVersion()).toBe('dev')
  stamp('')
  expect(appVersion()).toBe('dev')
})

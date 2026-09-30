import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { stamp } from './stamp-sw'

const source = readFileSync(join(__dirname, '..', 'public', 'sw.js'), 'utf8')

describe('stamp', () => {
  it('lists every built asset for install to precache', () => {
    const out = stamp(source, ['index-b.js', 'index-a.css'])
    expect(out).toContain('const ASSETS = ["/app/assets/index-a.css","/app/assets/index-b.js"]')
  })

  it('leaves the API docs bundle out of the precache', () => {
    const out = stamp(source, ['index-a.js', 'api-docs-c.js', 'api-docs-d.css'])
    expect(out).toContain('const ASSETS = ["/app/assets/index-a.js"]')
  })

  it('names the cache under the app prefix, by the asset names', () => {
    const a = stamp(source, ['index-a.js'])
    const b = stamp(source, ['index-b.js'])
    expect(a).toMatch(/^const CACHE = 'twillingate-app-[0-9a-f]{12}'$/m)
    expect(a).not.toEqual(b)
    expect(stamp(source, ['index-a.js'])).toEqual(a)
  })

  it('refuses a worker without the lines to stamp', () => {
    expect(() => stamp('const X = 1', [])).toThrow(/CACHE/)
  })
})

// run evaluates a stamped worker against a fake Cache Storage holding the
// given cache names and returns the handlers it registered plus what it did.
function run(sw: string, existing: string[]) {
  const handlers: Record<string, (event: unknown) => void> = {}
  const deleted: string[] = []
  const added: string[] = []
  const caches = {
    keys: async () => existing,
    delete: async (name: string) => (deleted.push(name), true),
    open: async () => ({ addAll: async (urls: string[]) => void added.push(...urls) }),
  }
  const self = { addEventListener: (type: string, fn: (event: unknown) => void) => (handlers[type] = fn) }
  new Function('self', 'caches', sw)(self, caches)
  const fire = async (type: string) => {
    let done: Promise<unknown> = Promise.resolve()
    handlers[type]({ waitUntil: (p: Promise<unknown>) => (done = p) })
    await done
  }
  return { fire, deleted, added }
}

describe('the stamped worker', () => {
  const sw = stamp(source, ['index-a.css', 'index-b.js'])
  const cache = /^const CACHE = '([^']*)'$/m.exec(sw)![1]

  it('precaches the shell and every built asset on install', async () => {
    const w = run(sw, [])
    await w.fire('install')
    expect(w.added).toEqual(['/app/', '/app/index.html', '/app/assets/index-a.css', '/app/assets/index-b.js'])
  })

  it('deletes only its own older caches on activate', async () => {
    const w = run(sw, [cache, 'twillingate-app-0123456789ab', 'other-app-v3', 'workbox-precache'])
    await w.fire('activate')
    expect(w.deleted).toEqual(['twillingate-app-0123456789ab'])
  })
})

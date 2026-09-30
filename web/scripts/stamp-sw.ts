// Stamps the built service worker with this build's cache name and asset
// list. The name is a short hash of the asset names (which Vite already
// content-hashes), so each release gets a new cache and the activate step
// evicts the old one; the list is what install precaches. Deterministic: the
// same build stamps the same bytes. Run as part of `npm run build`, after
// `vite build`.
import { createHash } from 'node:crypto'
import { readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const CACHE_LINE = /^const CACHE = '[^']*'$/m
const ASSETS_LINE = /^const ASSETS = \[.*\]$/m

// The API docs page's chunk (Swagger UI, ~1.5 MB) is left out of the
// precache: the dashboards never load it, so installing it would only cost
// every visitor the download.
const NOT_PRECACHED = /^ApiDocs-/

// stamp returns sw with its CACHE and ASSETS lines set for the given built
// asset file names (as listed in the assets directory).
export function stamp(sw: string, assets: string[]): string {
  if (!CACHE_LINE.test(sw)) throw new Error(`no "const CACHE = '…'" line to stamp`)
  if (!ASSETS_LINE.test(sw)) throw new Error('no "const ASSETS = […]" line to stamp')
  const names = assets.filter((name) => !NOT_PRECACHED.test(name)).sort()
  const version = createHash('sha256').update(names.join('\n')).digest('hex').slice(0, 12)
  const urls = names.map((name) => `/app/assets/${name}`)
  return sw
    .replace(CACHE_LINE, `const CACHE = 'twillingate-app-${version}'`)
    .replace(ASSETS_LINE, `const ASSETS = ${JSON.stringify(urls)}`)
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const outDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'internal', 'reporting', 'ui')
  const swFile = join(outDir, 'sw.js')
  writeFileSync(swFile, stamp(readFileSync(swFile, 'utf8'), readdirSync(join(outDir, 'assets'))))
}

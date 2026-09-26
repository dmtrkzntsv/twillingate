// Stamps the built service worker's cache name with a short hash of the
// built asset names (which Vite already content-hashes), so each release
// gets a new cache and the service worker's activate step evicts the old
// bundles. Deterministic: the same build stamps the same name. Run as part
// of `npm run build`, after `vite build`.
import { createHash } from 'node:crypto'
import { readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const outDir = join(scriptDir, '..', '..', 'internal', 'reporting', 'ui')
const swFile = join(outDir, 'sw.js')
const CACHE_LINE = /^const CACHE = '[^']*'$/m

const assets = readdirSync(join(outDir, 'assets')).sort()
const version = createHash('sha256').update(assets.join('\n')).digest('hex').slice(0, 12)

const sw = readFileSync(swFile, 'utf8')
if (!CACHE_LINE.test(sw)) throw new Error(`${swFile}: no "const CACHE = '…'" line to stamp`)
writeFileSync(swFile, sw.replace(CACHE_LINE, `const CACHE = 'app-${version}'`))

// Writes internal/reporting/ui/components.json: the widget contracts the Go
// server reads to validate dashboard definitions. Run as part of `npm run
// build`, after `vite build` so the file lands in the same, already-emptied
// output directory.
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { widgets } from '../src/components/widgets/index.ts'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const outFile = join(scriptDir, '..', '..', 'internal', 'reporting', 'ui', 'components.json')

const components = Object.entries(widgets)
  .map(([name, widget]) => ({
    name,
    description: widget.contract.description,
    accepts: widget.contract.accepts,
    inputs: widget.contract.inputs,
    props: widget.contract.props,
    default_width: widget.contract.defaultWidth,
    default_height: widget.contract.defaultHeight,
  }))
  .sort((a, b) => a.name.localeCompare(b.name))

mkdirSync(dirname(outFile), { recursive: true })
writeFileSync(outFile, `${JSON.stringify({ components }, null, 2)}\n`)

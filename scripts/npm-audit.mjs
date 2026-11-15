#!/usr/bin/env node
// The npm half of `make vuln` and of .github/workflows/vuln.yml.
//
//   node scripts/npm-audit.mjs <dir>...
//
// For each directory it runs `npm audit` twice. What ships (--omit=dev:
// the packages bundled into the dashboards app or the SDK) fails the run
// on any advisory of high or critical severity that <dir>/audit-allow.json
// does not list. The whole tree, development tools included, is printed
// for information and never fails: those packages run on a developer's
// machine and in CI, not in a release.
//
// audit-allow.json is {"GHSA-…": "why it is accepted for now"}; an entry
// whose advisory no longer applies fails the run too, so the list cannot
// outlive what it excuses.
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

function audit(dir, args) {
  let out
  try {
    out = execFileSync('npm', ['audit', '--json', ...args], { cwd: dir, encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] })
  } catch (e) {
    out = e.stdout // npm audit exits 1 when it finds anything
  }
  const report = JSON.parse(out || '{}')
  if (!report.vulnerabilities) throw new Error(`${dir}: npm audit gave no report: ${out}`)
  return report
}

// The advisories behind a report: id -> {severity, title, pkg}.
function advisories(report) {
  const found = new Map()
  for (const [pkg, v] of Object.entries(report.vulnerabilities)) {
    for (const via of v.via) {
      if (typeof via === 'string') continue // a dependency's advisory, listed under its own name
      const id = via.url?.split('/').pop() ?? String(via.source)
      found.set(id, { severity: via.severity, title: via.title, pkg })
    }
  }
  return found
}

let failed = false
for (const dir of process.argv.slice(2)) {
  const allowFile = join(dir, 'audit-allow.json')
  const allow = existsSync(allowFile) ? JSON.parse(readFileSync(allowFile, 'utf8')) : {}
  const shipped = advisories(audit(dir, ['--omit=dev']))
  for (const [id, a] of shipped) {
    if (a.severity !== 'high' && a.severity !== 'critical') continue
    if (allow[id]) {
      console.log(`${dir}: allowed ${id} (${a.pkg}, ${a.severity}): ${allow[id]}`)
    } else {
      console.error(`${dir}: ${a.severity} in a shipped dependency: ${a.pkg}: ${a.title} (${id})`)
      failed = true
    }
  }
  for (const id of Object.keys(allow)) {
    if (!shipped.has(id)) {
      console.error(`${dir}: ${allowFile} lists ${id}, which no longer applies: remove it`)
      failed = true
    }
  }
  const all = audit(dir, []).metadata.vulnerabilities
  console.log(`${dir}: whole tree, development tools included (information only): ` +
    `${all.critical} critical, ${all.high} high, ${all.moderate} moderate, ${all.low} low`)
}
process.exit(failed ? 1 : 0)

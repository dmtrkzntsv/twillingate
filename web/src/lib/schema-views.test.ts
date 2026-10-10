/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { parseSchemaViews } from './schema-views'

// jsdom replaces the URL global, so import.meta.url cannot build a file path here.
const goSource = readFileSync(resolve(import.meta.dirname, '../../../internal/api/resources.go'), 'utf8')
const text = goSource.match(/const schemaViews = `([\s\S]*?)`/)?.[1] ?? ''

describe('parseSchemaViews', () => {
  it('reads every view the API documents, with its columns', () => {
    const tables = parseSchemaViews(text)
    const names = tables.map((t) => t.name)
    const documented = [...text.matchAll(/^\s+(v_\w+|identities)\(/gm)].map((m) => m[1])
    expect(documented.length).toBeGreaterThan(15)
    expect(names).toEqual(documented)
    expect(tables.find((t) => t.name === 'v_views_daily')?.columns).toEqual([
      'project_id', 'day', 'kind', 'visitors', 'views', 'sessions', 'bounces', 'duration_sec',
    ])
  })
  it('ignores prose and trailing comments', () => {
    expect(parseSchemaViews('Facts:\n  v_x(a, b)  -- note (x)\n1. day columns (UTC)')).toEqual([{ name: 'v_x', columns: ['a', 'b'] }])
  })
})

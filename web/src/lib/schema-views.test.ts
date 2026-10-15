/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { parseSchemaViews } from './schema-views'
import { sqlNamespace } from './sql-completion'

// jsdom replaces the URL global, so import.meta.url cannot build a file path here.
const goSource = readFileSync(resolve(import.meta.dirname, '../../../internal/api/resources.go'), 'utf8')
const files = goSource.match(/const schemaFiles = `([\s\S]*?)`/)?.[1] ?? ''
const sqlite = goSource.match(/const schemaSQLite = `([\s\S]*?)`/)?.[1] ?? ''
// What GET /api/schema/views serves: the files, a blank line, the deprecated part.
const served = files + '\n\n' + sqlite

describe('parseSchemaViews', () => {
  it('reads the event files tables, with their columns, and stops before the SQLite views', () => {
    const tables = parseSchemaViews(served)
    const documented = [...files.matchAll(/^\s+(\w+)\(/gm)].map((m) => m[1])
    expect(documented).toEqual(['events', 'views', 'product', 'measures', 'sessions', 'identities', 'aliases'])
    expect(tables.map((t) => t.name)).toEqual(documented)
    expect(tables.find((t) => t.name === 'views')?.columns).toContain('actor_id')
    expect(tables.find((t) => t.name === 'measures')?.columns).toContain('bucket')
    expect(tables.find((t) => t.name === 'identities')?.columns).toEqual([
      'project_id', 'kind', 'id', 'name', 'first_seen', 'last_seen', 'views', 'events',
    ])
    expect(tables.some((t) => t.name.startsWith('v_'))).toBe(false)
  })
  it('ignores prose and trailing comments', () => {
    expect(parseSchemaViews('Facts:\n  x(a, b)  -- note (x)\n1. day columns (UTC)')).toEqual([{ name: 'x', columns: ['a', 'b'] }])
  })
  it('stops at the deprecated heading', () => {
    expect(parseSchemaViews('  a(x)\n\nDeprecated: query with source sqlite\n\n  b(y)')).toEqual([{ name: 'a', columns: ['x'] }])
  })
  it('completes the tables and their columns', () => {
    const ns = sqlNamespace(parseSchemaViews(served))
    expect(Object.keys(ns)).toContain('events')
    expect(ns.views).toContain('actor_id')
  })
})

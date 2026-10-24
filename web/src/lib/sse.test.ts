import { describe, expect, it } from 'vitest'
import { readSSE, SSEParser, type SSEEvent } from './sse'

describe('SSEParser', () => {
  it('reads events split anywhere across chunks', () => {
    const p = new SSEParser()
    const text = 'event: widget\ndata: {"a":1}\n\nevent: done\ndata: {}\n\n'
    const events = []
    for (const c of text) events.push(...p.push(c))
    expect(events).toEqual([
      { event: 'widget', data: '{"a":1}', id: '' },
      { event: 'done', data: '{}', id: '' },
    ])
  })

  it('takes CRLF and CR line ends, a CRLF split between chunks included', () => {
    const p = new SSEParser()
    expect(p.push('event: widget\r')).toEqual([])
    // The last CR may yet be a CRLF's: the blank line it ends waits for the next chunk.
    expect(p.push('\ndata: x\r\n\r')).toEqual([])
    expect(p.push('data: y\r\n\r\n')).toEqual([
      { event: 'widget', data: 'x', id: '' },
      { event: 'message', data: 'y', id: '' },
    ])
    expect(p.push('data: z\r\r')).toEqual([])
    expect(p.push('\n')).toEqual([{ event: 'message', data: 'z', id: '' }])
    expect(p.push('data: w\r\r\r\n')).toEqual([{ event: 'message', data: 'w', id: '' }])
  })

  it('skips comments and unknown fields, and dispatches nothing without data', () => {
    const p = new SSEParser()
    expect(p.push(': keepalive\n\n')).toEqual([])
    expect(p.push('event: widget\nid: 3\nretry: 10\n\n')).toEqual([])
    // The id set by the event that was never dispatched still holds.
    expect(p.push('event: widget\n: note\ndata:no-space\n\n')).toEqual([{ event: 'widget', data: 'no-space', id: '3' }])
  })

  it('joins several data lines with newlines', () => {
    expect(new SSEParser().push('data: a\ndata: b\n\n')).toEqual([{ event: 'message', data: 'a\nb', id: '' }])
  })

  it('drops an event the body ends inside', () => {
    const p = new SSEParser()
    expect(p.push('event: widget\ndata: x')).toEqual([])
    expect(p.end()).toEqual([])
  })
})

describe('SSEParser ids', () => {
  it('carries the last id over to later events until another is set', () => {
    const p = new SSEParser()
    expect(p.push('id: 7\ndata: a\n\ndata: b\n\nid\ndata: c\n\n')).toEqual([
      { event: 'message', data: 'a', id: '7' },
      { event: 'message', data: 'b', id: '7' },
      { event: 'message', data: 'c', id: '' },
    ])
  })
})

async function all(source: Response | ReadableStream<Uint8Array>): Promise<SSEEvent[]> {
  const out: SSEEvent[] = []
  for await (const e of readSSE(source)) out.push(e)
  return out
}

describe('readSSE', () => {
  it('yields the events of a response, a character split across chunks included', async () => {
    const bytes = new TextEncoder().encode('event: note\ndata: caf\u00e9\n\n')
    const split = new ReadableStream<Uint8Array>({
      start(c) {
        // The two bytes of é arrive apart.
        c.enqueue(bytes.slice(0, 25))
        c.enqueue(bytes.slice(25))
        c.close()
      },
    })
    expect(await all(new Response(split))).toEqual([{ event: 'note', data: 'caf\u00e9', id: '' }])
  })

  it('reads a bare body, and lets it go when the loop is left', async () => {
    let cancelled = false
    const enc = new TextEncoder()
    const stream = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(enc.encode(': hi\n\ndata: 1\n\n'))
      },
      cancel() {
        cancelled = true
      },
    })
    for await (const e of readSSE(stream)) {
      expect(e.data).toBe('1')
      break
    }
    expect(cancelled).toBe(true)
  })

  it('throws when the body breaks off', async () => {
    const broken = new ReadableStream<Uint8Array>({
      pull(c) {
        c.error(new Error('connection reset'))
      },
    })
    await expect(all(broken)).rejects.toThrow('connection reset')
  })
})

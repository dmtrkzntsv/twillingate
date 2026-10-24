/**
 * Server-Sent Events read from a `fetch` body, for streams an EventSource
 * cannot open (a POST, or one sent with the console's Authorization
 * header). It knows nothing of what the events carry.
 */

/**
 * One event: its type (`message` when unnamed), its data lines joined by
 * newlines, and the last event id the stream has set (`''` when none).
 */
export interface SSEEvent {
  event: string
  data: string
  id: string
}

/**
 * Parses an event stream as it arrives, chunk by chunk: a line or an event
 * split across chunks waits for the rest, lines end in LF, CRLF or CR,
 * comments (`: keepalive`) and unknown fields are skipped, and an event
 * with no data line is not dispatched, as EventSource does. The id carries
 * over to later events until the stream sets another.
 */
export class SSEParser {
  private buffer = ''
  // How far the buffer has been searched for a line end, so a long line
  // arriving in many chunks is scanned once, not once per chunk.
  private scanned = 0
  private event = ''
  private data: string[] = []
  private id = ''

  /** Feeds one chunk and returns the events it completes. */
  push(chunk: string): SSEEvent[] {
    this.buffer += chunk
    const out: SSEEvent[] = []
    let start = 0
    let i = this.scanned
    for (;;) {
      const lf = this.buffer.indexOf('\n', i)
      const cr = this.buffer.indexOf('\r', i)
      const end = lf < 0 ? cr : cr < 0 ? lf : Math.min(lf, cr)
      if (end < 0) {
        i = this.buffer.length
        break
      }
      // A CR last in the chunk may be the first half of a CRLF: wait for the next one.
      if (end === cr && end === this.buffer.length - 1) {
        i = end
        break
      }
      this.line(this.buffer.slice(start, end), out)
      i = end === cr && this.buffer[end + 1] === '\n' ? end + 2 : end + 1
      start = i
    }
    this.buffer = this.buffer.slice(start)
    this.scanned = i - start
    return out
  }

  /** The body has ended: a last line without its end is read, but an event without its blank line is dropped. */
  end(): SSEEvent[] {
    const out: SSEEvent[] = []
    if (this.buffer.length > 0) this.line(this.buffer.replace(/\r$/, ''), out)
    this.buffer = ''
    this.scanned = 0
    this.event = ''
    this.data = []
    return out
  }

  private line(line: string, out: SSEEvent[]) {
    if (line === '') {
      if (this.data.length > 0) out.push({ event: this.event || 'message', data: this.data.join('\n'), id: this.id })
      this.event = ''
      this.data = []
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon < 0 ? line : line.slice(0, colon)
    let value = colon < 0 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') this.event = value
    else if (field === 'data') this.data.push(value)
    else if (field === 'id' && !value.includes('\0')) this.id = value
  }
}

/**
 * Yields a stream's events as they arrive, from a `fetch` response or its
 * body. It ends with the body and throws when the body breaks off (an
 * aborted request, a dropped connection). Leaving the loop early lets go
 * of the body.
 */
export async function* readSSE(source: Response | ReadableStream<Uint8Array>): AsyncGenerator<SSEEvent> {
  const body = source instanceof Response ? source.body : source
  if (!body) throw new Error('the event stream has no body')
  const reader = body.getReader()
  const decoder = new TextDecoder()
  const parser = new SSEParser()
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) {
        yield* parser.push(decoder.decode())
        yield* parser.end()
        return
      }
      yield* parser.push(decoder.decode(value, { stream: true }))
    }
  } finally {
    // Read through or left early, the body is let go (a no-op once it has ended).
    void reader.cancel().catch(() => {})
  }
}

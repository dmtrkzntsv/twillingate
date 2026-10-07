import { crc32, deflateSync } from 'node:zlib'
import { expect, type APIRequestContext } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const TOKEN = 'e2e-token'

function chunk(type: string, data: Buffer): Buffer {
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data])
  const out = Buffer.alloc(body.length + 8)
  out.writeUInt32BE(data.length, 0)
  body.copy(out, 4)
  out.writeUInt32BE(crc32(body), body.length + 4)
  return out
}

/** A valid grey PNG of w×h (8-bit RGB, filter 0 rows), for seeding shares through the API. */
export function solidPng(w: number, h: number): Buffer {
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(w, 0)
  ihdr.writeUInt32BE(h, 4)
  ihdr[8] = 8 // bit depth
  ihdr[9] = 2 // colour type: RGB
  // The row is a filter byte (0) then w grey pixels; every row is the same.
  const row = Buffer.concat([Buffer.from([0]), Buffer.alloc(w * 3, 0x80)])
  const raw = Buffer.concat(Array.from({ length: h }, () => row))
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw)),
    chunk('IEND', Buffer.alloc(0)),
  ])
}

/** The pixel size a PNG's IHDR declares (bytes 16..24). */
export function pngSize(b: Buffer): { width: number; height: number } {
  return { width: b.readUInt32BE(16), height: b.readUInt32BE(20) }
}

/** Creates a share over REST with grey stand-in images, as the web app would upload its captures. */
export async function createShare(
  request: APIRequestContext,
  o: { widgetId: number; projectId: number; from: string; to: string }
): Promise<{ id: string; url: string }> {
  const file = (name: string, buffer: Buffer) => ({ name, mimeType: 'image/png', buffer })
  const res = await request.post('/api/widget-shares', {
    headers: { Authorization: `Bearer ${TOKEN}` },
    multipart: {
      widget_id: String(o.widgetId),
      project_id: String(o.projectId),
      from: o.from,
      to: o.to,
      image: file('image.png', solidPng(1200, 630)),
      image_2x: file('image_2x.png', solidPng(2400, 1260)),
    },
  })
  expect(res.status(), await res.text()).toBe(201)
  const { id, url } = (await res.json()) as { id: string; url: string }
  return { id, url }
}

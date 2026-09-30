import { useEffect, useRef } from 'react'
import SwaggerUI from 'swagger-ui-dist/swagger-ui-es-bundle.js'
import 'swagger-ui-dist/swagger-ui.css'
import { OPENAPI_URL, withAppAuth } from '@/lib/api-docs'
import { refreshAccess } from '@/lib/auth'

/**
 * `/api-docs`: Swagger UI over the REST API's OpenAPI document. Loaded on
 * its own chunk, which the service worker does not precache: the dashboards
 * never need it.
 */
export default function ApiDocs() {
  const root = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!root.current) return
    SwaggerUI({
      domNode: root.current,
      url: OPENAPI_URL,
      deepLinking: true,
      requestInterceptor: withAppAuth,
      // A 401 is most likely an expired access token: refresh it so the
      // next try is signed with a fresh one.
      responseInterceptor: async (res) => {
        if (res.status === 401) await refreshAccess()
        return res
      },
    })
  }, [])
  // Swagger UI is light-only: keep it light inside a dark app.
  return <div ref={root} className="min-h-svh bg-white pb-8 text-black [color-scheme:light]" />
}

// The /api/docs page: Swagger UI over the REST API's OpenAPI document, a
// page of its own rather than a route of the app (see api-docs.html). The
// service worker does not precache it: the dashboards never load it.
import SwaggerUI from 'swagger-ui-dist/swagger-ui-es-bundle.js'
import 'swagger-ui-dist/index.css'
import 'swagger-ui-dist/swagger-ui.css'
import { OPENAPI_URL, withAppAuth } from '@/lib/api-docs'
import { refreshAccess } from '@/lib/auth'

SwaggerUI({
  domNode: document.getElementById('swagger-ui')!,
  url: OPENAPI_URL,
  deepLinking: true,
  requestInterceptor: withAppAuth,
  // A 401 is most likely an expired access token: refresh it so the next
  // try is signed with a fresh one.
  responseInterceptor: async (res) => {
    if (res.status === 401) await refreshAccess()
    return res
  },
})

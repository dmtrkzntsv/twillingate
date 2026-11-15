// The /api/docs page: Swagger UI over the REST API's OpenAPI document, a
// page of its own rather than a route of the app (see api-docs.html). The
// service worker does not precache it: the dashboards never load it.
import SwaggerUI from 'swagger-ui-dist/swagger-ui-es-bundle.js'
import 'swagger-ui-dist/index.css'
import 'swagger-ui-dist/swagger-ui.css'
import { OPENAPI_URL, withAppAuth } from '@/lib/api-docs'
import { refreshAccess } from '@/lib/auth'
import { adoptCredentials } from '@/lib/credentials'

// The page usually opens in a tab of its own, which starts without the
// app's credentials: ask the app's tabs for them first.
void adoptCredentials().then(() => {
  SwaggerUI({
    domNode: document.getElementById('swagger-ui')!,
    url: OPENAPI_URL,
    deepLinking: true,
    // No validator badge: it would send the document's address to
    // validator.swagger.io, which the page's policy refuses anyway.
    validatorUrl: null,
    requestInterceptor: withAppAuth,
    // A 401 is most likely an expired access token: refresh it so the next
    // try is signed with a fresh one.
    responseInterceptor: async (res) => {
      if (res.status === 401) await refreshAccess()
      return res
    },
  })
})

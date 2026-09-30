// swagger-ui-dist ships no types; this is the slice ApiDocs uses.
declare module 'swagger-ui-dist/swagger-ui-es-bundle.js' {
  interface SwaggerRequest {
    url: string
    headers: Record<string, string>
  }
  interface SwaggerResponse {
    status: number
  }
  interface SwaggerUIOptions {
    domNode: HTMLElement
    url: string
    deepLinking?: boolean
    tryItOutEnabled?: boolean
    requestInterceptor?: (req: SwaggerRequest) => SwaggerRequest | Promise<SwaggerRequest>
    responseInterceptor?: (res: SwaggerResponse) => SwaggerResponse | Promise<SwaggerResponse>
  }
  export default function SwaggerUI(options: SwaggerUIOptions): unknown
}

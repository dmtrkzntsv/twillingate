/**
 * The version of the binary serving the app, which the Go server stamps into
 * the shell's `twillingate-version` tag (internal/reporting/ui.go). The tag
 * reads "dev" as built, so `npm run dev`, which serves the unstamped page,
 * shows "dev".
 */
export function appVersion(): string {
  return document.querySelector<HTMLMetaElement>('meta[name="twillingate-version"]')?.content || 'dev'
}

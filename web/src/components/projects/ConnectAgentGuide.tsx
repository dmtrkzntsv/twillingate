import { currentAuthState } from '@/lib/auth'
import CodeBlock from './CodeBlock'

const PROMPT = 'Add twillingate analytics to my site.'
const DOCS = 'https://github.com/dmtrkzntsv/twillingate/blob/main/docs/deployment.md#connect-a-client'

/**
 * The empty Projects page: how to connect an AI agent, which then does the
 * rest. `consoleURL` is CONSOLE_URL; a server without one is reached at this
 * page's own address. A browser holding a pasted token is on a server with no
 * sign-in page, where an agent sends that token rather than signing in.
 */
export default function ConnectAgentGuide({ consoleURL }: { consoleURL?: string }) {
  const url = `${consoleURL || window.location.origin}/mcp`
  const signsIn = currentAuthState().kind !== 'token'
  return (
    <section aria-label="Connect your AI agent" className="flex max-w-2xl flex-col gap-4 rounded-lg border p-4">
      <div className="flex flex-col gap-1">
        <h2 className="text-base font-semibold">Connect your AI agent</h2>
        <p className="text-sm text-muted-foreground">Your agent can create projects, add the tracking code to your site and build dashboards.</p>
      </div>
      <div className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">1. Add this address to your agent</h3>
        <CodeBlock value={url} label="Copy MCP address" />
        {signsIn ? (
          <>
            <ul className="flex list-disc flex-col gap-2 pl-5 text-sm text-muted-foreground">
              <li><strong className="font-medium text-foreground">claude.ai, Claude Desktop:</strong> Settings → Connectors → Add custom connector. Leave the client ID and secret empty.</li>
              <li><strong className="font-medium text-foreground">ChatGPT:</strong> add it as a connector, with OAuth authentication.</li>
              <li><strong className="font-medium text-foreground">Claude Code:</strong> run this, then <code className="font-mono text-xs">/mcp</code> → Authenticate.</li>
            </ul>
            <CodeBlock value={`claude mcp add --transport http twillingate ${url}`} label="Copy Claude Code command" />
            <ul className="flex list-disc flex-col gap-2 pl-5 text-sm text-muted-foreground">
              <li><strong className="font-medium text-foreground">Codex and others:</strong> add it as an MCP server, then authenticate.</li>
            </ul>
            <p className="text-sm text-muted-foreground">When your agent asks you to sign in, use the same login as this page.</p>
          </>
        ) : (
          <p className="text-sm text-muted-foreground">
            This server uses a fixed token instead of a sign-in page, so your agent has to send it as an{' '}
            <code className="font-mono text-xs">Authorization: Bearer</code> header. See{' '}
            <a href={DOCS} target="_blank" rel="noopener" className="underline underline-offset-2 hover:text-foreground">Connect a client</a>.
          </p>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">2. Ask it to set up your site</h3>
        <CodeBlock value={PROMPT} label="Copy prompt" />
      </div>
      <p className="text-sm text-muted-foreground">Prefer to do it by hand? Use New project above.</p>
    </section>
  )
}

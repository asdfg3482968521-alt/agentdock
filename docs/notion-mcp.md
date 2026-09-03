# Connect AgentDock to Notion MCP

AgentDock can connect to the official Notion MCP endpoint through an OAuth-capable stdio bridge. The first connection requires an interactive Notion sign-in; no Notion API secret is stored in AgentDock's MCP registry.

## Requirements

- Node.js 18 or newer
- `npx` available in the AgentDock process `PATH`
- A browser session on the machine running AgentDock for the first OAuth approval

## Recommended setup

The built-in Notion preset expands this minimal request into a pinned `mcp-remote` bridge with a five-minute authorization timeout:

```json
{
  "action": "add",
  "name": "notion"
}
```

Then start discovery and OAuth:

```json
{
  "action": "refresh",
  "name": "notion"
}
```

On first refresh, complete the browser prompt: sign in to Notion, select the workspace, and approve access. `mcp-remote` stores the OAuth session under the AgentDock host user's `~/.mcp-auth` directory, so later starts normally reuse it.

## Explicit configuration

The equivalent explicit registration is:

```json
{
  "action": "add",
  "name": "notion",
  "description": "Official Notion MCP via OAuth",
  "transport": "stdio",
  "command": "npx",
  "args": [
    "-y",
    "mcp-remote@0.1.38",
    "https://mcp.notion.com/mcp",
    "--transport",
    "http-only",
    "--auth-timeout",
    "300",
    "--body-timeout",
    "0",
    "--keep-alive"
  ],
  "enabled": true,
  "timeout_ms": 300000
}
```

Do not include Markdown angle brackets around the endpoint. AgentDock now repairs copied values such as `<https://mcp.notion.com/mcp>`, but the canonical value is `https://mcp.notion.com/mcp`.

A direct unauthenticated `streamable_http` registration for this endpoint is automatically converted to the OAuth bridge because AgentDock's native remote client does not yet drive interactive OAuth.

## Verify the connection

1. Search the Notion server's tools:

   ```json
   {
     "server": "notion",
     "query": "search pages"
   }
   ```

2. Inspect a returned qualified tool name such as `notion:<tool>`.
3. Call it with arguments matching the inspected schema.

## Headless hosts and containers

The default OAuth callback is loopback-based. If AgentDock runs on a remote headless host, authorize once from an interactive session on that host (or use secure port forwarding) before using the dynamic MCP connection. Notion still requires user approval; this change does not bypass OAuth.

If authentication state is stale, stop AgentDock and remove only the relevant `mcp-remote` state under `~/.mcp-auth`, then refresh and authorize again. Removing the whole directory signs out every MCP server using `mcp-remote` for that host user.

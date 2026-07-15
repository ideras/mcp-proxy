# Usage

## CLI

```text
-config string         path to config file or a http(s) url (default "config.json")
-expand-env            expand environment variables in config file (default true)
-http-headers string   optional headers for config URL: 'Key1:Value1;Key2:Value2'
-http-timeout int      timeout (seconds) for remote config fetch (default 10)
-insecure              skip TLS verification for remote config
-version               print version and exit
-help                  print help and exit
```

## Endpoints

Given `mcpProxy.baseURL = https://mcp.example.com` and a server key `fetch`:

- For `type: sse`: `https://mcp.example.com/fetch/sse`
- For `type: streamable-http`: `https://mcp.example.com/fetch/mcp`

## Auth

If `options.authTokens` is set for a server, requests must include a bearer token:

```
Authorization: <token>
```

If your client cannot set headers, embed the token in the route key (e.g. `fetch/<token>`) and call that path instead.

## Aggregating MCPs under one URL (groups)

By default each `mcpServers` entry is a separate endpoint. A `groups` entry
merges several members behind **one** URL so a client sees the union of their
tools/prompts/resources.

Config:

```jsonc
{
  "mcpProxy": { "baseURL": "https://mcp.example.com", "addr": ":9090", "name": "Proxy", "version": "1.0.0", "type": "streamable-http" },
  "mcpServers": {
    "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] },
    "fetch":  { "command": "uvx", "args": ["mcp-server-fetch"] }
  },
  "groups": {
    "all": { "servers": ["github", "fetch"], "conflictMode": "prefix" }
  }
}
```

With `baseURL = https://mcp.example.com` and group key `all`:

- For `type: sse`: `https://mcp.example.com/all/sse`
- For `type: streamable-http`: `https://mcp.example.com/all/mcp`

The individual `/github/` and `/fetch/` routes are **not** published because
those members belong to a group.

With `conflictMode: "prefix"` (the default), tools are namespaced by member
name to avoid collisions — e.g. if both backends expose a `search` tool, the
merged endpoint offers `github.search` and `fetch.search`, each dispatched to
the correct backend. Use `"error"` to fail loudly on duplicate names, or
`"first-wins"` to keep whichever member registered first. See
[CONFIGURATION.md](CONFIGURATION.md#groups-aggregate--unify-mcps-under-one-url)
for the full option reference.


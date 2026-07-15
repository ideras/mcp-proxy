# Configuration

This project supports a v2 JSON configuration. v1 configs are automatically migrated at load time.

- Online converter (build Claude config from your proxy): https://tbxark.github.io/mcp-proxy

## Full Example

```jsonc
{
  "mcpProxy": {
    "baseURL": "https://mcp.example.com",
    "addr": ":9090",
    "name": "MCP Proxy",
    "version": "1.0.0",
    "type": "streamable-http", // or "sse" (default)
    "options": {
      "panicIfInvalid": false,
      "logEnabled": true,
      "authTokens": ["DefaultToken"]
    }
  },
  "mcpServers": {
    "github": {
      // stdio client
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "<YOUR_TOKEN>" },
      "options": {
        "toolFilter": {
          "mode": "block",
          "list": ["create_or_update_file"]
        }
      }
    },
    "fetch": {
      // stdio client
      "command": "uvx",
      "args": ["mcp-server-fetch"],
      "options": {
        "panicIfInvalid": true,
        "logEnabled": false,
        "authTokens": ["SpecificToken"]
      }
    },
    "amap": {
      // SSE client
      "url": "https://mcp.amap.com/sse?key=<YOUR_TOKEN>",
      "options": {
        "disabled": true
      }
    }
  }
}
```

## mcpProxy

- `baseURL`: Public URL base used to build client endpoints.
- `addr`: Bind address (e.g. `:9090`).
- `name`, `version`: Server identity for MCP handshake.
- `type`: `sse` (default) or `streamable-http`.
- `options`: Defaults inherited by `mcpServers.*.options` (can be overridden per server).

## mcpServers

Each entry defines a downstream MCP server. Supported client types:

- `stdio` (implicit when `command` is set): run a subprocess via stdio.
- `sse` (implicit when `url` is set and `transportType` ≠ `streamable-http`): connect via Server‑Sent Events.
- `streamable-http` (requires `transportType: "streamable-http"`): connect via HTTP streaming.

Common fields:

- `command`, `args`, `env` — for `stdio` clients.
- `url`, `headers` — for `sse` and `streamable-http` clients.
- `timeout` — request timeout for `streamable-http`.
- `options` — per‑server overrides and filters (see below).

## options

- `panicIfInvalid` (bool): If true, startup fails when a client cannot initialize.
- `logEnabled` (bool): Log requests and events for this client.
- `authTokens` ([]string): Valid bearer tokens; requests must include `Authorization: <token>`.
- `toolFilter` (object): Selectively expose tools to the proxy:
  - `mode`: `allow` or `block`.
  - `list`: List of tool names.
- `Disabled` (bool): Enable or disable this server. Disabled servers are skipped at startup.

Notes:

- `mcpProxy.options.authTokens` serves as the default token set if a server omits `options.authTokens`.
- To discover tool names for filtering, start without a filter and check logs for lines like `<server> Adding tool <name>`.

## groups (aggregate / unify MCPs under one URL)

Normally each `mcpServers` entry is published on its **own** sub-route
(`<baseURL>/<name>/`). `groups` instead merges several entries into a **single**
MCP endpoint so a client connects to one URL and sees the union of all member
tools/prompts/resources.

Members of a group are **not** exposed on their own standalone route — they are
only reachable through the group route. A `mcpServers` entry may belong to at
most one group.

```jsonc
{
  "mcpProxy": { /* ...as above... */ },
  "mcpServers": {
    "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] },
    "fetch":  { "command": "uvx", "args": ["mcp-server-fetch"] }
  },
  "groups": {
    "all": {
      "servers": ["github", "fetch"],
      "conflictMode": "prefix",
      "options": { "authTokens": ["GroupToken"] }
    }
  }
}
```

- **`servers`** (`[]string`, required, non-empty): the `mcpServers` keys to merge.
- **`conflictMode`** (default `"prefix"`): what to do when two members expose
  the same tool/prompt/resource:
  - `"prefix"` — namespace every member's items with its own name
    (`github.search`, `fetch.search`) so duplicates never collide. The proxy
    transparently strips the prefix before calling the backend, so backends are
    untouched.
  - `"error"` — register names verbatim and **fail startup** on the first
    duplicate. Use when you assert your members have no overlapping names and
    want clean, unprefixed names.
  - `"first-wins"` — register verbatim and keep whichever member registered a
    name first, silently skipping later duplicates.
- **`options`**: per-group `OptionsV2` (same shape as `mcpProxy.options`).
  These defaults inherit from `mcpProxy.options` the same way per-server
  options do, and the group's middlewares (recover / logging / auth) govern the
  single merged route. Per-member `options.toolFilter` and `options.disabled`
  still apply *before* tools are merged into the group.

### Conflict-mode escaping

Conflict handling applies to **tools** (by name), **prompts** (by name),
**resources** (by URI), and **resource templates** (by URI template). In
`"prefix"` mode:

- tools & prompts become `<member>.<name>`;
- resource URIs become `<member>/<uri>`;
- resource-template patterns become `<member>/<template>`.

The proxy restores the original name/URI before delegating to the backend,
so member servers need no awareness of namespacing.


# search-web-mcp

Stdio MCP server exposing a `search_web` tool that proxies to the Brave Search
and Tavily Search APIs.

- Multiple backends, enabled independently by API key
- Routing: `round_robin` (default) or `priority`, with failover on errors
- 429 handling: a backend that answers 429 is skipped for a cooldown that
  doubles on repeat 429s (cap 7 days) until a probe succeeds or the provider
  resets; state persists across restarts
- `status` tool reports per-backend state without consuming provider calls

## Requirements

- Go 1.25+
- A Brave and/or Tavily API key (at least one)

## Setup

```sh
export BRAVE_API_KEY=...
export TAVILY_API_KEY=...   # at least one required
```

## Build & run

```sh
make build          # -> bin/search-web-mcp
./bin/search-web-mcp

# or without make:
go run ./cmd/search-web-mcp
```

## MCP client config

```json
{
  "mcpServers": {
    "search-web": {
      "command": "/abs/path/to/search-web-mcp",
      "env": { "BRAVE_API_KEY": "...", "TAVILY_API_KEY": "..." }
    }
  }
}
```

## Env vars

| Var | Default | Purpose |
|---|---|---|
| `BRAVE_API_KEY` | — | enable Brave |
| `TAVILY_API_KEY` | — | enable Tavily |
| `SEARCH_MCP_ROUTING` | `round_robin` | `round_robin` or `priority` |
| `SEARCH_MCP_PRIORITY` | `brave,tavily` | order for priority routing |
| `SEARCH_MCP_COOLDOWN` | `1h` | initial 429 cooldown |
| `SEARCH_MCP_TIMEOUT` | `20s` | upstream request timeout |
| `SEARCH_MCP_STATE_FILE` | `~/.local/state/search-web-mcp/state.json` | throttle state |
| `SEARCH_MCP_BRAVE_BASE_URL` | `https://api.search.brave.com` | test override |
| `SEARCH_MCP_TAVILY_BASE_URL` | `https://api.tavily.com` | test override |

## Tools

- `search_web(query, max_results=10, backend=auto|brave|tavily, time_range=day|week|month|year, country, language)`
- `status` — per-backend configured/throttle state, no provider call

---
title: "ADR-001: SDK-Agnostic Core Package"
status: accepted
date: 2024-12-01
---

## Context

oauth-mcp-proxy needs to support multiple MCP SDK implementations: the community `mark3labs/mcp-go`
(v0.41.1) and the official `modelcontextprotocol/go-sdk` (v1.0.0). These SDKs have different APIs:
mark3labs uses `server.ToolHandlerFunc` middleware and `ServerOption`, while the official SDK uses
standard `http.Handler` wrapping with `mcp.NewStreamableHTTPHandler`.

Coupling OAuth logic to a single SDK would require duplication when supporting the other, and would
create vendor lock-in that prevents users from migrating between SDKs.

## Decision

The core package (root `oauth` package) contains all OAuth logic and is SDK-agnostic. It must not
import any MCP SDK. SDK-specific integration lives in adapter packages:

- **`mark3labs/`** — Imports `mark3labs/mcp-go`, provides `WithOAuth(mux, cfg) -> (*Server, ServerOption, error)`.
  Uses `server.WithToolHandlerMiddleware()` to inject the authentication middleware.
- **`mcp/`** — Imports `modelcontextprotocol/go-sdk`, provides `WithOAuth(mux, cfg, mcpServer) -> (*Server, http.Handler, error)`.
  Wraps `mcp.NewStreamableHTTPHandler` with Bearer token validation.

Core types used by adapters:
- `oauth.Server` — Created via `NewServer(cfg)`, holds validator + cache + handler
- `oauth.ValidateTokenCached(ctx, token)` — Cache-aware token validation
- `oauth.WithOAuthToken(ctx, token)` / `oauth.WithUser(ctx, user)` — Context helpers
- `oauth.RegisterHandlers(mux)` — Registers OAuth HTTP endpoints

Note: The root package `oauth.go` does import `mark3labs/mcp-go` for the legacy `WithOAuth()` and
`Middleware()` functions. The `mark3labs/` adapter is the recommended path for new code.

## Consequences

**Positive:**
- Users choose their SDK without changing OAuth config or learning a different API.
- Core logic is tested once, not per-SDK. Adapter tests focus on SDK wiring.
- Adding a third SDK adapter requires only a new directory, no core changes.

**Negative:**
- Adapters duplicate some wiring code (Bearer extraction, 401 responses). The `mcp/oauth.go` adapter
  re-implements Bearer validation inline rather than reusing `WrapHandler()` because it needs to
  control the full HTTP lifecycle for the official SDK's handler.
- The root package still has a legacy mark3labs dependency via `Middleware()` and `GetHTTPServerOptions()`.

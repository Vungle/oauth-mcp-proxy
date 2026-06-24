---
title: "ADR-005: Context-Based User Propagation"
status: accepted
date: 2024-12-01
---

## Context

Authenticated user information must flow from the HTTP layer (where Bearer tokens are extracted)
through the MCP middleware layer (where tokens are validated) to the tool handler (where the user
identity is consumed). Go's MCP SDKs use `context.Context` as the primary mechanism for passing
request-scoped data through handler chains.

The library must avoid global state (multiple Server instances may coexist) and must work with both
mark3labs and official SDK handler signatures.

## Decision

Two-phase context propagation using typed context keys (context.go):

**Phase 1 — Token extraction** (HTTP layer):
`CreateHTTPContextFunc()` (middleware.go:120-142) extracts the Bearer token from the Authorization
header and stores it in context:
```go
ctx = WithOAuthToken(ctx, token)  // context.go:14-16
```

**Phase 2 — Token validation and user injection** (middleware layer):
The SDK-specific middleware retrieves the token, validates it, and adds the user:
```go
tokenString, ok := GetOAuthToken(ctx)          // context.go:19-22
user, err := s.ValidateTokenCached(ctx, token)
ctx = WithUser(ctx, user)                       // context.go:25-27
```

**Consumer access** (tool handler):
```go
user, ok := GetUserFromContext(ctx)  // context.go:42-44
```

Context keys are typed constants to prevent collisions (context.go:7-11):
```go
type contextKey string
const (
    oauthTokenKey  contextKey = "oauth_token"
    userContextKey contextKey = "user"
)
```

The mark3labs adapter (mark3labs/middleware.go) uses `server.ToolHandlerFunc` signature and calls
`oauth.GetOAuthToken` -> `ValidateTokenCached` -> `oauth.WithUser`. The official SDK adapter
(mcp/oauth.go) does the same but at the HTTP handler level before passing to
`mcp.NewStreamableHTTPHandler`.

## Consequences

**Positive:**
- Request-scoped: each request carries its own user, no global state or goroutine-local storage.
- Works identically with both SDK adapters since both use `context.Context`.
- Type-safe keys prevent collision with other context values.
- Tool handlers have a clean API: `GetUserFromContext(ctx)` returns `(*User, bool)`.

**Negative:**
- Two-phase design means a missing Phase 1 (no `CreateHTTPContextFunc`) silently fails at Phase 2
  with "authentication required: missing OAuth token" — not always obvious to debug.
- Context values are not visible in type signatures, so the dependency on `WithOAuthToken` being
  called before middleware is implicit.

## Alternatives Considered

- **Global user map keyed by request ID**: Rejected because it introduces global state and cleanup complexity.
- **Custom request struct wrapping context**: Rejected because it would require non-standard handler
  signatures incompatible with both SDKs.
- **Single-phase (validate at HTTP level only)**: Rejected because mark3labs SDK needs middleware at
  the tool handler level to access `mcp.CallToolRequest`.

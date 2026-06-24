---
title: "ADR-002: Dual-Mode Authentication (Native vs Proxy)"
status: accepted
date: 2024-12-01
---

## Context

MCP clients have varying OAuth capabilities. Some clients (like Claude Desktop) can perform their own
OAuth flows and send Bearer tokens directly. Others lack built-in OAuth support and need the server to
act as an OAuth proxy, handling the authorization code flow on their behalf.

The library needs to support both scenarios without requiring separate server configurations.

## Decision

Two authentication modes, auto-detected from configuration:

**Native mode** (token validation only):
- Client obtains token from OAuth provider independently.
- Server validates Bearer token via configured provider (HMAC or OIDC).
- Auto-selected when `ClientID` is empty.
- Only `/.well-known/*` metadata endpoints are registered.

**Proxy mode** (server-driven OAuth flow):
- Server proxies the full OAuth 2.1 authorization code flow.
- Registers additional endpoints: `/oauth/authorize`, `/oauth/callback`, `/oauth/token`, `/oauth/register`.
- Requires `ClientID`, `ClientSecret`, `ServerURL`, and `RedirectURIs` in config.
- Auto-selected when `ClientID` is present.

Auto-detection in `Config.Validate()` (config.go:39-45):
```go
if c.Mode == "" {
    if c.ClientID != "" {
        c.Mode = "proxy"
    } else {
        c.Mode = "native"
    }
}
```

Proxy mode validates additional requirements (config.go:77-87): ClientID, ServerURL, and RedirectURIs
must all be set.

## Consequences

**Positive:**
- Zero-config mode detection: users set credentials for their scenario and mode is inferred.
- Native mode has minimal surface area (no proxy endpoints exposed).
- Both modes share the same token validation and caching infrastructure.

**Negative:**
- Mode auto-detection can surprise users who set ClientID for a different reason.
- Proxy mode requires more config validation, making error messages harder to understand.

## Alternatives Considered

- **Single mode only**: Rejected because it would force all clients to have OAuth capability.
- **Explicit mode required**: Considered, but auto-detection from ClientID is intuitive and reduces
  configuration burden.

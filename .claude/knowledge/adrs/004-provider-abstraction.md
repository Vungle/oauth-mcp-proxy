---
title: "ADR-004: TokenValidator Interface for Provider Abstraction"
status: accepted
date: 2024-12-01
---

## Context

The library needs to support multiple OAuth providers: HMAC (for development/testing), and OIDC-based
providers (Okta, Google, Azure AD) for production. Each provider has fundamentally different validation
mechanisms: HMAC uses local JWT signature verification, while OIDC requires network-based JWKS
discovery and verification.

Tests also need to validate OAuth flows without real provider infrastructure.

## Decision

A `TokenValidator` interface in `provider/provider.go` abstracts all provider differences:

```go
type TokenValidator interface {
    ValidateToken(ctx context.Context, token string) (*User, error)
    Initialize(cfg *Config) error
}
```

Two implementations:
- **`HMACValidator`** — Local JWT validation using `golang-jwt/jwt/v5`. Uses `sync.Once` for
  initialization. No network I/O, making it fast and suitable for dev/testing. Validates HMAC-SHA256
  signature, expiry, not-before, issued-at, and audience claims.
- **`OIDCValidator`** — Network-based validation using `coreos/go-oidc/v3`. Initializes with 30-second
  timeout for OIDC discovery. Validates tokens via JWKS with 10-second timeout. Supports RS256 and
  ES256 signing algorithms. Enforces TLS 1.2+.

Both validators explicitly check the `aud` (audience) claim — this is security-critical and not
delegated to the JWT library alone.

Factory in `config.go` (createValidator, line 119):
```go
switch cfg.Provider {
case "hmac":
    validator = &provider.HMACValidator{}
case "okta", "google", "azure":
    validator = &provider.OIDCValidator{}
}
```

The `User` struct (provider/provider.go:17-21) is the common return type:
```go
type User struct {
    Username string
    Email    string
    Subject  string
}
```

## Consequences

**Positive:**
- Tests use `HMACValidator` or mock implementations without any OIDC infrastructure.
- Adding a new provider (e.g., Auth0) requires only implementing the interface and adding a case
  to `createValidator()`.
- Core package and adapters depend on the interface, not concrete implementations.
- `User` struct is re-exported from root package via type alias (`type User = provider.User` in cache.go).

**Negative:**
- `Initialize()` method means validators are not ready at construction time — must be called before use.
- The `ctx` parameter on `HMACValidator.ValidateToken` is unused (accepted for interface compliance).
- Provider-specific config (e.g., OIDC-specific TLS settings) is passed through the generic
  `provider.Config` struct.

## Alternatives Considered

- **Single validator with config switches**: Rejected because HMAC and OIDC have fundamentally
  different dependencies and initialization patterns.
- **Separate packages per provider**: Considered, but the current single-file approach
  (`provider/provider.go`) is appropriate given only two implementations.

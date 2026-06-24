---
title: "ADR-003: Token Caching with 5-Minute TTL and SHA-256 Keys"
status: accepted
date: 2024-12-01
---

## Context

Token validation can be expensive: OIDC validation requires network calls to the provider's JWKS
endpoint (10-second timeout per call). In a high-request environment, validating the same token on
every request would create unacceptable latency and load on the identity provider.

The cache must be thread-safe (concurrent HTTP requests), secure (no raw token storage), and
time-bounded (tokens should not be cached indefinitely after revocation).

## Decision

Each `Server` instance maintains its own `TokenCache` (cache.go). Key design choices:

**SHA-256 hash keys**: Raw tokens are never stored. The cache key is the full hex-encoded SHA-256
hash of the token (oauth.go:114):
```go
tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
```

**5-minute TTL**: Cached tokens expire after 5 minutes (oauth.go:129):
```go
expiresAt := time.Now().Add(5 * time.Minute)
```

**sync.RWMutex**: Read-heavy workload (most requests hit cache) uses `RLock` for reads and `Lock`
for writes (cache.go:15-16):
```go
type TokenCache struct {
    mu    sync.RWMutex
    cache map[string]*CachedToken
}
```

**Lazy cleanup**: Expired tokens are cleaned up asynchronously. When `getCachedToken` finds an expired
entry, it returns cache-miss and spawns a goroutine to delete it (cache.go:37-38):
```go
go tc.deleteExpiredToken(tokenHash)
```
The `deleteExpiredToken` goroutine re-checks expiry under write lock to prevent race conditions.

**Instance-scoped**: No global cache. Each `Server` creates its own cache in `NewServer()` (oauth.go:63-65).

## Consequences

**Positive:**
- OIDC validation cost amortized across requests with the same token.
- SHA-256 keys mean a cache dump does not leak tokens.
- RWMutex allows concurrent cache reads without contention.
- 5-minute TTL limits exposure window after token revocation.

**Negative:**
- Revoked tokens remain valid for up to 5 minutes.
- No cache size limit — a large number of unique tokens could grow memory (acceptable for MCP servers
  which typically have few concurrent users).
- Lazy cleanup means expired entries may persist briefly (not a correctness issue).

## Alternatives Considered

- **No cache**: Rejected because OIDC calls add 100-500ms latency per request.
- **Longer TTL (30min)**: Rejected because it widens the revocation window too much.
- **LRU cache with size limit**: Considered but over-engineered for MCP server use cases.

# Service Tokens

Service tokens let non-interactive agents call an MCP server without a browser OAuth flow. They are optional and run alongside the existing OAuth/OIDC provider.

## Token Contract

Service tokens are asymmetric JWTs. The MCP server verifies them with a public key; it never receives the private key.

Required claims:

- `iss`: must match `ServiceTokenIssuer`
- `aud`: must match `ServiceTokenAudience`
- `sub`: must start with `ServiceTokenSubjectPrefix` (default: `svc-`)
- `exp`: required; expired tokens are rejected

Supported signing algorithms:

- `EdDSA` with an Ed25519 public key
- `RS256` with an RSA public key

Do not use HMAC/HS256 for service tokens. HMAC requires the MCP server to hold a shared secret that can also mint tokens.

## Configuration

```go
oauthServer, oauthOption, err := mark3labs.WithOAuth(mux, &oauth.Config{
    Provider: "okta",
    Issuer:   "https://your-company.okta.com",
    Audience: "https://your-company.okta.com",

    ServiceTokenEnabled:       true,
    ServiceTokenIssuer:        "phoebe-service",
    ServiceTokenAudience:      "api://phoebe-mcp",
    ServiceTokenPublicKeyPEM:  publicKeyPEM,
    ServiceTokenSubjectPrefix: "svc-",
})
```

`ServiceTokenPublicKeyPEM` should contain only a public key. Keep the matching private key in a separate, restricted secret store used only by the token minting workflow.

## Environment Variables

`FromEnv()` also supports:

```text
SERVICE_TOKEN_ENABLED=true
SERVICE_TOKEN_ISSUER=phoebe-service
SERVICE_TOKEN_AUDIENCE=api://phoebe-mcp
SERVICE_TOKEN_PUBLIC_KEY_PEM=<PEM public key>
SERVICE_TOKEN_PUBLIC_KEY_PEM_B64=<base64-encoded PEM public key>
SERVICE_TOKEN_SUBJECT_PREFIX=svc-
```

Use `SERVICE_TOKEN_PUBLIC_KEY_PEM_B64` for Kubernetes and Terraform values to avoid multiline PEM escaping issues.

## Validation Flow

The validator first peeks at the unsigned `iss` claim only to choose the validator:

- `iss == ServiceTokenIssuer`: validate with the service-token public key and required claims.
- Any other issuer: validate with the configured OAuth/OIDC provider.

The peeked claim is not trusted for authorization. If a token claims the service issuer but fails service-token validation, it is rejected and does not fall back to the OAuth provider.

## Key Rotation

First version supports one active public key. Rotation is:

1. Mint new tokens with a new private key.
2. Deploy the matching new public key to MCP servers.
3. Replace agent tokens.
4. Stop accepting the old public key.

Keep service-token TTLs short enough that this rotation window is acceptable.

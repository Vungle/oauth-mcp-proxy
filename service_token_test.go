package oauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testPrimaryAudience = "api://primary"
	testServiceIssuer   = "agent-auth-service"
	testServiceAudience = "api://example-mcp-server"
)

func TestServiceTokenValidation(t *testing.T) {
	publicKeyPEM, privateKey := generateEd25519KeyPair(t)
	server := newServiceTokenTestServer(t, publicKeyPEM)

	token := signServiceToken(t, privateKey, jwt.MapClaims{
		"iss":                testServiceIssuer,
		"aud":                testServiceAudience,
		"sub":                "svc-example-agent",
		"preferred_username": "svc-example-agent",
		"exp":                time.Now().Add(time.Hour).Unix(),
	})

	user, err := server.ValidateTokenCached(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateTokenCached() error = %v", err)
	}
	if user.Subject != "svc-example-agent" {
		t.Fatalf("Subject = %q, want svc-example-agent", user.Subject)
	}
	if user.Username != "svc-example-agent" {
		t.Fatalf("Username = %q, want svc-example-agent", user.Username)
	}
}

func TestServiceTokenValidationRS256(t *testing.T) {
	publicKeyPEM, privateKey := generateRSAKeyPair(t)
	server := newServiceTokenTestServer(t, publicKeyPEM)

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": testServiceIssuer,
		"aud": testServiceAudience,
		"sub": "svc-example-agent",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}

	user, err := server.ValidateTokenCached(context.Background(), tokenString)
	if err != nil {
		t.Fatalf("ValidateTokenCached() error = %v", err)
	}
	if user.Subject != "svc-example-agent" {
		t.Fatalf("Subject = %q, want svc-example-agent", user.Subject)
	}
}

func TestServiceTokenRejectsInvalidTokens(t *testing.T) {
	publicKeyPEM, privateKey := generateEd25519KeyPair(t)
	_, wrongPrivateKey := generateEd25519KeyPair(t)
	server := newServiceTokenTestServer(t, publicKeyPEM)

	tests := []struct {
		name   string
		claims jwt.MapClaims
		key    ed25519.PrivateKey
	}{
		{
			name: "wrong signature",
			claims: jwt.MapClaims{
				"iss": testServiceIssuer,
				"aud": testServiceAudience,
				"sub": "svc-example-agent",
				"exp": time.Now().Add(time.Hour).Unix(),
			},
			key: wrongPrivateKey,
		},
		{
			name: "wrong issuer",
			claims: jwt.MapClaims{
				"iss": "other-service",
				"aud": testServiceAudience,
				"sub": "svc-example-agent",
				"exp": time.Now().Add(time.Hour).Unix(),
			},
			key: privateKey,
		},
		{
			name: "wrong audience",
			claims: jwt.MapClaims{
				"iss": testServiceIssuer,
				"aud": "api://other",
				"sub": "svc-example-agent",
				"exp": time.Now().Add(time.Hour).Unix(),
			},
			key: privateKey,
		},
		{
			name: "expired",
			claims: jwt.MapClaims{
				"iss": testServiceIssuer,
				"aud": testServiceAudience,
				"sub": "svc-example-agent",
				"exp": time.Now().Add(-time.Hour).Unix(),
			},
			key: privateKey,
		},
		{
			name: "missing expiry",
			claims: jwt.MapClaims{
				"iss": testServiceIssuer,
				"aud": testServiceAudience,
				"sub": "svc-example-agent",
			},
			key: privateKey,
		},
		{
			name: "invalid subject prefix",
			claims: jwt.MapClaims{
				"iss": testServiceIssuer,
				"aud": testServiceAudience,
				"sub": "example-agent",
				"exp": time.Now().Add(time.Hour).Unix(),
			},
			key: privateKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := signServiceToken(t, tt.key, tt.claims)
			if _, err := server.ValidateTokenCached(context.Background(), token); err == nil {
				t.Fatal("ValidateTokenCached() error = nil, want error")
			}
		})
	}
}

func TestServiceTokenRejectsSymmetricAlgorithm(t *testing.T) {
	publicKeyPEM, _ := generateEd25519KeyPair(t)
	server := newServiceTokenTestServer(t, publicKeyPEM)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": testServiceIssuer,
		"aud": testServiceAudience,
		"sub": "svc-example-agent",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenString, err := token.SignedString([]byte("shared-secret"))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}

	if _, err := server.ValidateTokenCached(context.Background(), tokenString); err == nil {
		t.Fatal("ValidateTokenCached() error = nil, want error")
	}
}

func TestServiceTokenRoutingFallsBackToPrimaryValidator(t *testing.T) {
	publicKeyPEM, _ := generateEd25519KeyPair(t)
	server := newServiceTokenTestServer(t, publicKeyPEM)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":                "human-issuer",
		"aud":                testPrimaryAudience,
		"sub":                "user-123",
		"preferred_username": "user@example.com",
		"exp":                time.Now().Add(time.Hour).Unix(),
	})
	tokenString, err := token.SignedString([]byte("primary-secret"))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}

	user, err := server.ValidateTokenCached(context.Background(), tokenString)
	if err != nil {
		t.Fatalf("ValidateTokenCached() error = %v", err)
	}
	if user.Subject != "user-123" {
		t.Fatalf("Subject = %q, want user-123", user.Subject)
	}
}

func TestServiceTokenConfigValidation(t *testing.T) {
	cfg := &Config{
		Provider:             "hmac",
		Audience:             testPrimaryAudience,
		JWTSecret:            []byte("primary-secret"),
		ServiceTokenEnabled:  true,
		ServiceTokenIssuer:   testServiceIssuer,
		ServiceTokenAudience: testServiceAudience,
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "service token public key PEM is required") {
		t.Fatalf("Validate() error = %v, want missing public key error", err)
	}
}

func TestFromEnvServiceTokenPublicKeyBase64(t *testing.T) {
	publicKeyPEM, _ := generateEd25519KeyPair(t)

	t.Setenv("OAUTH_PROVIDER", "hmac")
	t.Setenv("OIDC_AUDIENCE", testPrimaryAudience)
	t.Setenv("JWT_SECRET", "primary-secret")
	t.Setenv("SERVICE_TOKEN_ENABLED", "true")
	t.Setenv("SERVICE_TOKEN_ISSUER", testServiceIssuer)
	t.Setenv("SERVICE_TOKEN_AUDIENCE", testServiceAudience)
	t.Setenv("SERVICE_TOKEN_PUBLIC_KEY_PEM_B64", base64.StdEncoding.EncodeToString([]byte(publicKeyPEM)))

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ServiceTokenPublicKeyPEM != publicKeyPEM {
		t.Fatal("ServiceTokenPublicKeyPEM was not decoded from SERVICE_TOKEN_PUBLIC_KEY_PEM_B64")
	}
	if cfg.ServiceTokenSubjectPrefix != "svc-" {
		t.Fatalf("ServiceTokenSubjectPrefix = %q, want svc-", cfg.ServiceTokenSubjectPrefix)
	}
}

func generateEd25519KeyPair(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}

	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), privateKey
}

func generateRSAKeyPair(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}

	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), privateKey
}

func signServiceToken(t *testing.T, privateKey ed25519.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return tokenString
}

func newServiceTokenTestServer(t *testing.T, publicKeyPEM string) *Server {
	t.Helper()

	server, err := NewServer(&Config{
		Provider:                  "hmac",
		Audience:                  testPrimaryAudience,
		JWTSecret:                 []byte("primary-secret"),
		ServiceTokenEnabled:       true,
		ServiceTokenIssuer:        testServiceIssuer,
		ServiceTokenAudience:      testServiceAudience,
		ServiceTokenPublicKeyPEM:  publicKeyPEM,
		ServiceTokenSubjectPrefix: "svc-",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

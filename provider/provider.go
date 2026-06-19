package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
)

// User represents an authenticated user
type User struct {
	Username string
	Email    string
	Subject  string
}

// Logger interface for pluggable logging
type Logger interface {
	Debug(msg string, args ...interface{})
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
	Error(msg string, args ...interface{})
}

// Config holds OAuth configuration (subset needed by provider)
type Config struct {
	Provider                  string
	Issuer                    string
	Audience                  string
	JWTSecret                 []byte
	ServiceTokenIssuer        string
	ServiceTokenAudience      string
	ServiceTokenPublicKeyPEM  string
	ServiceTokenSubjectPrefix string
	Logger                    Logger
}

// TokenValidator interface for OAuth token validation
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (*User, error)
	Initialize(cfg *Config) error
}

// HMACValidator validates JWT tokens using HMAC-SHA256 (backward compatibility)
type HMACValidator struct {
	secret     string
	audience   string
	secretOnce sync.Once
}

// OIDCValidator validates JWT tokens using OIDC/JWKS (Okta, Google, Azure)
type OIDCValidator struct {
	verifier *oidc.IDTokenVerifier
	provider *oidc.Provider
	audience string
	logger   Logger
}

// ServiceTokenValidator validates asymmetric JWTs for non-interactive service access.
type ServiceTokenValidator struct {
	issuer        string
	audience      string
	subjectPrefix string
	publicKey     any
}

// RoutingValidator routes service-token JWTs to ServiceTokenValidator and all
// other tokens to the primary OAuth provider validator.
type RoutingValidator struct {
	primary       TokenValidator
	serviceToken  TokenValidator
	serviceIssuer string
}

// NewRoutingValidator returns a validator that supports both interactive OAuth
// provider tokens and non-interactive service tokens.
func NewRoutingValidator(primary TokenValidator, serviceToken TokenValidator, serviceIssuer string) TokenValidator {
	return &RoutingValidator{
		primary:       primary,
		serviceToken:  serviceToken,
		serviceIssuer: serviceIssuer,
	}
}

// ValidateToken validates service-token issuer JWTs with the service-token
// validator and all other tokens with the primary validator.
func (v *RoutingValidator) ValidateToken(ctx context.Context, tokenString string) (*User, error) {
	if peekJWTIssuer(tokenString) == v.serviceIssuer {
		return v.serviceToken.ValidateToken(ctx, tokenString)
	}
	return v.primary.ValidateToken(ctx, tokenString)
}

// Initialize is not used; child validators are initialized before routing.
func (v *RoutingValidator) Initialize(cfg *Config) error {
	return nil
}

// Initialize sets up the asymmetric service-token validator.
func (v *ServiceTokenValidator) Initialize(cfg *Config) error {
	v.issuer = cfg.ServiceTokenIssuer
	v.audience = cfg.ServiceTokenAudience
	v.subjectPrefix = cfg.ServiceTokenSubjectPrefix

	if v.issuer == "" {
		return fmt.Errorf("service token issuer is required")
	}
	if v.audience == "" {
		return fmt.Errorf("service token audience is required")
	}
	if v.subjectPrefix == "" {
		return fmt.Errorf("service token subject prefix is required")
	}

	publicKey, err := parseServiceTokenPublicKey(cfg.ServiceTokenPublicKeyPEM)
	if err != nil {
		return err
	}
	v.publicKey = publicKey
	return nil
}

// ValidateToken validates an EdDSA or RS256 service JWT using the configured public key.
func (v *ServiceTokenValidator) ValidateToken(ctx context.Context, tokenString string) (*User, error) {
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	claims := &struct {
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
		jwt.RegisteredClaims
	}{}

	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		func(token *jwt.Token) (interface{}, error) {
			switch token.Method.Alg() {
			case jwt.SigningMethodEdDSA.Alg():
				if key, ok := v.publicKey.(ed25519.PublicKey); ok {
					return key, nil
				}
			case jwt.SigningMethodRS256.Alg():
				if key, ok := v.publicKey.(*rsa.PublicKey); ok {
					return key, nil
				}
			}
			return nil, fmt.Errorf("unexpected signing method: %s", token.Method.Alg())
		},
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse and validate service token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid service token")
	}
	if claims.Subject == "" {
		return nil, fmt.Errorf("missing subject in service token")
	}
	if !strings.HasPrefix(claims.Subject, v.subjectPrefix) {
		return nil, fmt.Errorf("service token subject must start with %q", v.subjectPrefix)
	}

	username := claims.PreferredUsername
	if username == "" {
		username = claims.Subject
	}

	return &User{
		Subject:  claims.Subject,
		Username: username,
		Email:    claims.Email,
	}, nil
}

func parseServiceTokenPublicKey(publicKeyPEM string) (any, error) {
	if strings.TrimSpace(publicKeyPEM) == "" {
		return nil, fmt.Errorf("service token public key PEM is required")
	}

	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("failed to decode service token public key PEM")
	}

	if block.Type == "RSA PUBLIC KEY" {
		key, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse RSA service token public key: %w", err)
		}
		return key, nil
	}

	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse service token public key: %w", err)
	}

	switch key := key.(type) {
	case ed25519.PublicKey:
		return key, nil
	case *rsa.PublicKey:
		return key, nil
	default:
		return nil, fmt.Errorf("unsupported service token public key type %T", key)
	}
}

func peekJWTIssuer(tokenString string) string {
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	parser := jwt.NewParser()
	claims := jwt.MapClaims{}
	if _, _, err := parser.ParseUnverified(tokenString, claims); err != nil {
		return ""
	}
	return getStringClaim(claims, "iss")
}

// Initialize sets up the HMAC validator with JWT secret and audience
func (v *HMACValidator) Initialize(cfg *Config) error {
	v.secretOnce.Do(func() {
		v.secret = string(cfg.JWTSecret)
		v.audience = cfg.Audience
	})

	if v.secret == "" {
		return fmt.Errorf("JWT_SECRET is required for HMAC provider")
	}

	if v.audience == "" {
		return fmt.Errorf("JWT audience is required for HMAC provider")
	}

	return nil
}

// ValidateToken validates JWT token using HMAC-SHA256
func (v *HMACValidator) ValidateToken(ctx context.Context, tokenString string) (*User, error) {
	// Note: ctx parameter accepted for interface compliance, but HMAC validation is local-only (no I/O)
	// Remove Bearer prefix if present
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	// Parse and validate JWT with signature verification
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(v.secret), nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse and validate token: %w", err)
	}

	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}

	// Validate required claims including audience
	if err := validateTokenClaims(claims); err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	// Validate audience claim for security
	if err := v.validateAudience(claims); err != nil {
		return nil, fmt.Errorf("audience validation failed: %w", err)
	}

	// Extract user information
	user := &User{
		Subject:  getStringClaim(claims, "sub"),
		Username: getStringClaim(claims, "preferred_username"),
		Email:    getStringClaim(claims, "email"),
	}

	if user.Subject == "" {
		return nil, fmt.Errorf("missing subject in token")
	}

	return user, nil
}

// validateAudience validates the audience claim matches the expected value
func (v *HMACValidator) validateAudience(claims jwt.MapClaims) error {
	// Extract audience claim (can be string or []string)
	audClaim, exists := claims["aud"]
	if !exists {
		return fmt.Errorf("missing audience claim")
	}

	// Handle string audience
	if audStr, ok := audClaim.(string); ok {
		if audStr != v.audience {
			return fmt.Errorf("invalid audience: expected %s, got %s", v.audience, audStr)
		}
		return nil
	}

	// Handle array of audiences
	if audArray, ok := audClaim.([]interface{}); ok {
		for _, aud := range audArray {
			if audStr, ok := aud.(string); ok && audStr == v.audience {
				return nil
			}
		}
		return fmt.Errorf("invalid audience: expected %s not found in audience list", v.audience)
	}

	return fmt.Errorf("invalid audience claim type")
}

// Initialize sets up the OIDC validator with provider discovery
func (v *OIDCValidator) Initialize(cfg *Config) error {
	if cfg.Issuer == "" {
		return fmt.Errorf("OIDC issuer is required for OIDC provider")
	}
	if cfg.Audience == "" {
		return fmt.Errorf("OIDC audience is required for OIDC provider")
	}

	v.logger = cfg.Logger
	if v.logger == nil {
		v.logger = &noOpLogger{}
	}
	v.audience = cfg.Audience

	// Use standard library context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Configure HTTP client with appropriate timeouts and TLS settings
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false, // Verify TLS certificates
				MinVersion:         tls.VersionTLS12,
			},
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
		},
	}

	// Create OIDC provider with custom HTTP client
	provider, err := oidc.NewProvider(
		oidc.ClientContext(ctx, httpClient),
		cfg.Issuer,
	)
	if err != nil {
		return fmt.Errorf("failed to initialize OIDC provider: %w", err)
	}

	// Configure token verifier with required validation settings
	verifier := provider.Verifier(&oidc.Config{
		ClientID:             cfg.Audience, // Note: go-oidc uses ClientID field for audience validation - see https://github.com/coreos/go-oidc/blob/v3/oidc/verify.go#L85
		SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256},
		SkipClientIDCheck:    false, // Always validate if ClientID is provided
		SkipExpiryCheck:      false, // Verify expiration
		SkipIssuerCheck:      false, // Verify issuer
	})

	v.logger.Info("OAuth: OIDC validator initialized with audience validation: %s", cfg.Audience)

	v.provider = provider
	v.verifier = verifier
	return nil
}

// ValidateToken validates JWT token using OIDC/JWKS
func (v *OIDCValidator) ValidateToken(ctx context.Context, tokenString string) (*User, error) {
	// Remove Bearer prefix if present
	tokenString = strings.TrimPrefix(tokenString, "Bearer ")

	// Use incoming context with timeout for OIDC provider call
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// go-oidc handles RSA signature validation, JWKS fetching, and key rotation
	idToken, err := v.verifier.Verify(ctx, tokenString)
	if err != nil {
		return nil, fmt.Errorf("token verification failed: %w", err)
	}

	// Extract claims from verified token
	var claims struct {
		Subject           string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified,omitempty"`
		Name              string `json:"name,omitempty"`
		// Standard OIDC claims are validated by go-oidc:
		// - iss (issuer)
		// - aud (audience)
		// - exp (expiration)
		// - iat (issued at)
		// - nbf (not before)
	}

	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("failed to extract claims: %w", err)
	}

	// Extract raw claims for audience validation
	var rawClaims jwt.MapClaims
	if err := idToken.Claims(&rawClaims); err != nil {
		return nil, fmt.Errorf("failed to extract raw claims: %w", err)
	}

	// Validate audience claim for security (explicit check)
	if err := v.validateAudience(rawClaims); err != nil {
		return nil, fmt.Errorf("audience validation failed: %w", err)
	}

	return &User{
		Subject:  claims.Subject,
		Username: claims.PreferredUsername,
		Email:    claims.Email,
	}, nil
}

// validateAudience validates the audience claim matches the expected value for OIDC tokens
func (v *OIDCValidator) validateAudience(claims jwt.MapClaims) error {
	// Extract audience claim (can be string or []string)
	audClaim, exists := claims["aud"]
	if !exists {
		return fmt.Errorf("missing audience claim")
	}

	// Handle string audience
	if audStr, ok := audClaim.(string); ok {
		if audStr != v.audience {
			return fmt.Errorf("invalid audience: expected %s, got %s", v.audience, audStr)
		}
		return nil
	}

	// Handle array of audiences
	if audArray, ok := audClaim.([]interface{}); ok {
		for _, aud := range audArray {
			if audStr, ok := aud.(string); ok && audStr == v.audience {
				return nil
			}
		}
		return fmt.Errorf("invalid audience: expected %s not found in audience list", v.audience)
	}

	return fmt.Errorf("invalid audience claim type")
}

// validateTokenClaims validates standard JWT claims
func validateTokenClaims(claims jwt.MapClaims) error {
	// Validate expiration
	if exp, ok := claims["exp"]; ok {
		if expTime, ok := exp.(float64); ok {
			if time.Now().Unix() > int64(expTime) {
				return fmt.Errorf("token expired")
			}
		}
	}

	// Validate not before
	if nbf, ok := claims["nbf"]; ok {
		if nbfTime, ok := nbf.(float64); ok {
			if time.Now().Unix() < int64(nbfTime) {
				return fmt.Errorf("token not yet valid")
			}
		}
	}

	// Validate issued at (should not be in the future)
	if iat, ok := claims["iat"]; ok {
		if iatTime, ok := iat.(float64); ok {
			if time.Now().Unix() < int64(iatTime) {
				return fmt.Errorf("token issued in the future")
			}
		}
	}

	return nil
}

// getStringClaim safely extracts a string claim
func getStringClaim(claims jwt.MapClaims, key string) string {
	if val, ok := claims[key].(string); ok {
		return val
	}
	return ""
}

// noOpLogger is a no-op logger used when cfg.Logger is nil
type noOpLogger struct{}

func (l *noOpLogger) Debug(msg string, args ...interface{}) {}
func (l *noOpLogger) Info(msg string, args ...interface{})  {}
func (l *noOpLogger) Warn(msg string, args ...interface{})  {}
func (l *noOpLogger) Error(msg string, args ...interface{}) {}

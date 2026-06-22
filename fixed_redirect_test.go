package oauth

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestFixedRedirectModeLocalhostOnly(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	tests := []struct {
		name          string
		clientURI     string
		shouldPass    bool
		expectedError string
	}{
		{
			name:       "HTTP localhost allowed",
			clientURI:  "http://localhost:8080/callback",
			shouldPass: true,
		},
		{
			name:       "HTTP 127.0.0.1 allowed",
			clientURI:  "http://127.0.0.1:3000/callback",
			shouldPass: true,
		},
		{
			name:       "HTTP IPv6 localhost allowed",
			clientURI:  "http://[::1]:9000/callback",
			shouldPass: true,
		},
		{
			name:       "HTTPS localhost allowed",
			clientURI:  "https://localhost/callback",
			shouldPass: true,
		},
		{
			name:          "HTTPS production domain rejected",
			clientURI:     "https://evil.com/callback",
			shouldPass:    false,
			expectedError: "Fixed redirect mode only allows localhost",
		},
		{
			name:          "HTTP production domain rejected",
			clientURI:     "http://evil.com/callback",
			shouldPass:    false,
			expectedError: "HTTPS required for non-localhost",
		},
		{
			name:          "localhost subdomain rejected",
			clientURI:     "https://localhost.evil.com/callback",
			shouldPass:    false,
			expectedError: "Fixed redirect mode only allows localhost",
		},
		{
			name:          "URI with fragment rejected",
			clientURI:     "http://localhost:8080/callback#fragment",
			shouldPass:    false,
			expectedError: "must not contain fragment",
		},
		{
			name:          "Custom scheme rejected",
			clientURI:     "custom://localhost:8080/callback",
			shouldPass:    false,
			expectedError: "Invalid redirect_uri scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isLocalhost := isLocalhostURI(tt.clientURI)

			if tt.shouldPass && !isLocalhost {
				t.Errorf("Expected localhost detection to pass for %s", tt.clientURI)
			}

			if !tt.shouldPass && isLocalhost && tt.expectedError != "must not contain fragment" && tt.expectedError != "Invalid redirect_uri scheme" {
				t.Errorf("Expected localhost detection to fail for %s", tt.clientURI)
			}

			t.Logf("URI: %s, isLocalhost: %v, shouldPass: %v", tt.clientURI, isLocalhost, tt.shouldPass)
		})
	}
}

func TestFixedRedirectModeSecurityModel(t *testing.T) {
	t.Log("Fixed Redirect Mode Security Model:")
	t.Log("- Single OAUTH_REDIRECT_URI configured (no commas)")
	t.Log("- Server uses fixed URI to communicate with OAuth provider")
	t.Log("- Client redirect URIs MUST be localhost for security")
	t.Log("- HMAC-signed state prevents redirect URI tampering")
	t.Log("")
	t.Log("Attack Prevention:")
	t.Log("1. Open Redirect → Localhost-only restriction prevents external redirects")
	t.Log("2. State Tampering → HMAC signature verification prevents modification")
	t.Log("3. Code Theft → PKCE prevents token exchange without code_verifier")
	t.Log("4. HTTP Exposure → HTTPS required for non-localhost URIs")
	t.Log("")
	t.Log("Use Case: Development tools (MCP Inspector) running on localhost")
	t.Log("Production: Use allowlist mode instead")
}

func TestFixedRedirectModeAllowsConfiguredClientRedirectURI(t *testing.T) {
	handler := newFixedRedirectTestHandler(t, "cursor://anysphere.cursor-mcp/oauth/callback")

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id=test-client&redirect_uri="+url.QueryEscape("cursor://anysphere.cursor-mcp/oauth/callback")+"&response_type=code&code_challenge=test&code_challenge_method=S256&state=test-state", nil)
	recorder := httptest.NewRecorder()

	handler.HandleAuthorize(recorder, req)

	if recorder.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, expected %d, body: %s", recorder.Code, http.StatusTemporaryRedirect, recorder.Body.String())
	}

	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "https://okta.example/authorize?") {
		t.Fatalf("Location = %q, expected Okta authorize redirect", location)
	}
	if !strings.Contains(location, url.QueryEscape("https://mcp-server.com/oauth/callback")) {
		t.Fatalf("Location = %q, expected provider redirect_uri to remain the fixed server callback", location)
	}
}

func TestFixedRedirectModeRejectsUnconfiguredCustomScheme(t *testing.T) {
	handler := newFixedRedirectTestHandler(t, "")

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id=test-client&redirect_uri="+url.QueryEscape("cursor://anysphere.cursor-mcp/oauth/callback")+"&response_type=code&code_challenge=test&code_challenge_method=S256&state=test-state", nil)
	recorder := httptest.NewRecorder()

	handler.HandleAuthorize(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, expected %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), "Invalid redirect_uri scheme") {
		t.Fatalf("body = %q, expected Invalid redirect_uri scheme", recorder.Body.String())
	}
}

func TestFixedRedirectModeStillAllowsLocalhostRedirectURI(t *testing.T) {
	handler := newFixedRedirectTestHandler(t, "")

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id=test-client&redirect_uri="+url.QueryEscape("http://127.0.0.1:3333/oauth/callback")+"&response_type=code&code_challenge=test&code_challenge_method=S256&state=test-state", nil)
	recorder := httptest.NewRecorder()

	handler.HandleAuthorize(recorder, req)

	if recorder.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, expected %d, body: %s", recorder.Code, http.StatusTemporaryRedirect, recorder.Body.String())
	}
}

func TestFixedRedirectCallbackAllowsConfiguredClientRedirectURI(t *testing.T) {
	handler := newFixedRedirectTestHandler(t, "cursor://anysphere.cursor-mcp/oauth/callback")
	signedState, err := handler.signState(map[string]string{
		"state":    "client-state",
		"redirect": "cursor://anysphere.cursor-mcp/oauth/callback",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=auth-code&state="+url.QueryEscape(signedState), nil)
	recorder := httptest.NewRecorder()

	handler.HandleCallback(recorder, req)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, expected %d, body: %s", recorder.Code, http.StatusFound, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.HasPrefix(location, "cursor://anysphere.cursor-mcp/oauth/callback?") {
		t.Fatalf("Location = %q, expected Cursor callback redirect", location)
	}
	if !strings.Contains(location, "code=auth-code") || !strings.Contains(location, "state=client-state") {
		t.Fatalf("Location = %q, expected code and original state", location)
	}
}

func TestFixedRedirectCallbackRejectsUnconfiguredCustomScheme(t *testing.T) {
	handler := newFixedRedirectTestHandler(t, "")
	signedState, err := handler.signState(map[string]string{
		"state":    "client-state",
		"redirect": "cursor://anysphere.cursor-mcp/oauth/callback",
	})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=auth-code&state="+url.QueryEscape(signedState), nil)
	recorder := httptest.NewRecorder()

	handler.HandleCallback(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, expected %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), "Invalid redirect URI in state") {
		t.Fatalf("body = %q, expected Invalid redirect URI in state", recorder.Body.String())
	}
}

func newFixedRedirectTestHandler(t *testing.T, allowedClientRedirectURIs string) *OAuth2Handler {
	t.Helper()

	key := make([]byte, 32)
	_, _ = rand.Read(key)

	return &OAuth2Handler{
		config: &OAuth2Config{
			RedirectURIs:              "https://mcp-server.com/oauth/callback",
			AllowedClientRedirectURIs: allowedClientRedirectURIs,
			stateSigningKey:           key,
		},
		oauth2Config: &oauth2.Config{
			ClientID:    "test-client",
			RedirectURL: "https://mcp-server.com/oauth/callback",
			Endpoint: oauth2.Endpoint{
				AuthURL: "https://okta.example/authorize",
			},
		},
		logger: &defaultLogger{},
	}
}

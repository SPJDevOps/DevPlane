package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Stable JSON error codes for machine-readable API responses.
const (
	AuthErrorCodeUnauthorized = "unauthorized"
	AuthErrorCodeForbidden    = "forbidden"
	// AuthErrorCodeTokenExpired is returned when the OIDC ID token is past exp (after clock skew).
	AuthErrorCodeTokenExpired = "token_expired"
	// WorkspaceErrorCodeUnavailable is returned when the gateway cannot read or create the Workspace CR.
	WorkspaceErrorCodeUnavailable = "workspace_unavailable"
	// WorkspaceErrorCodeNotReady is returned when the workspace pod is not listening on ttyd yet.
	WorkspaceErrorCodeNotReady = "workspace_not_ready"
	// RateLimitErrorCode is returned with HTTP 429 when a gateway rate limit is exceeded.
	RateLimitErrorCode = "rate_limited"
)

// WriteJSONAuthError writes {"error": code} with Content-Type application/json.
func WriteJSONAuthError(w http.ResponseWriter, status int, code string) {
	WriteJSONError(w, status, code)
}

// WriteJSONError writes {"error": code} with Content-Type application/json.
func WriteJSONError(w http.ResponseWriter, status int, code string) {
	RecordJSONAPIError(status, code)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(map[string]string{"error": code})
}

// AuthErrorResponse interprets errors returned from token validation for HTTP APIs.
func AuthErrorResponse(err error) (status int, code string) {
	if err == nil {
		return http.StatusOK, ""
	}
	if errors.Is(err, ErrForbidden) {
		return http.StatusForbidden, AuthErrorCodeForbidden
	}
	if errors.Is(err, ErrTokenExpired) {
		return http.StatusUnauthorized, AuthErrorCodeTokenExpired
	}
	return http.StatusUnauthorized, AuthErrorCodeUnauthorized
}

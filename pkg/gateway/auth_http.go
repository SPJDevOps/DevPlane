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
)

// WriteJSONAuthError writes {"error": code} with Content-Type application/json.
func WriteJSONAuthError(w http.ResponseWriter, status int, code string) {
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
	return http.StatusUnauthorized, AuthErrorCodeUnauthorized
}

package gateway

import (
	"testing"
)

func TestSanitizeUserID(t *testing.T) {
	tests := []struct {
		name string
		sub  string
		want string
	}{
		{name: "simple", sub: "john", want: "john"},
		{name: "pipe separator", sub: "auth0|12345", want: "auth0-12345"},
		{name: "uppercase", sub: "John.Doe", want: "john-doe"},
		{name: "multiple special", sub: "user@example.com", want: "user-example-com"},
		{name: "leading trailing special", sub: "---john---", want: "john"},
		{name: "long subject truncated", sub: string(make([]byte, 100)), want: func() string {
			// 100 null bytes → sanitized to empty after trim
			return ""
		}()},
		{name: "long alphanumeric", sub: "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz01234567890", want: "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeUserID(tt.sub)
			if got != tt.want {
				t.Errorf("sanitizeUserID(%q) = %q, want %q", tt.sub, got, tt.want)
			}
		})
	}
}

func TestHashToken(t *testing.T) {
	h1 := hashToken("token-a")
	h2 := hashToken("token-b")
	if h1 == h2 {
		t.Error("different tokens should produce different hashes")
	}
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64 (sha256 hex)", len(h1))
	}
	// Deterministic
	if hashToken("token-a") != h1 {
		t.Error("hashToken should be deterministic")
	}
}

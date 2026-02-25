package gateway

import (
	"container/list"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		// Truncated at 49 (= 63 RFC-1035 max − 14 for "-workspace-svc" suffix).
		{name: "long alphanumeric", sub: "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz01234567890", want: "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklm"},
		// Keycloak subs are UUIDs; ~62% start with a hex digit (0-9).
		{name: "uuid digit-first", sub: "12345678-abcd-efef-1234-abcdefabcdef", want: "u-12345678-abcd-efef-1234-abcdefabcdef"},
		{name: "uuid letter-first", sub: "f47ac10b-58cc-4372-a567-0e02b2c3d479", want: "f47ac10b-58cc-4372-a567-0e02b2c3d479"},
		// digit-first: after "u-" prefix the string is 50 chars; truncation at 49
		// lands on "-", which TrimRight removes.
		{
			name: "digit-first truncation trims trailing hyphen",
			sub:  "9" + strings.Repeat("a", 45) + "-b",
			want: "u-9" + strings.Repeat("a", 45),
		},
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

// TestNewValidator creates a minimal fake OIDC discovery server so that
// NewValidator can complete its provider-discovery HTTP round-trip without
// a real IdP.
func TestNewValidator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := "http://" + r.Host
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 issuer,
				"authorization_endpoint": issuer + "/auth",
				"token_endpoint":         issuer + "/token",
				"jwks_uri":               issuer + "/jwks",
			})
		case "/jwks":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"keys": []interface{}{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stop the evictExpired goroutine

	v, err := NewValidator(ctx, srv.URL, "test-client")
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	if v == nil {
		t.Fatal("NewValidator returned nil")
	}
}

// TestValidate_CacheHit seeds the in-memory LRU cache directly then calls
// Validate to exercise the fast path that returns cached claims without
// contacting the OIDC verifier.
func TestValidate_CacheHit(t *testing.T) {
	v := &Validator{
		index: make(map[string]*list.Element),
		lru:   list.New(),
	}

	rawToken := "cached-bearer-token"
	key := hashToken(rawToken)
	want := &Claims{Sub: "user1", Email: "user1@example.com", UserID: "user1"}

	entry := &cachedEntry{
		key:    key,
		claims: want,
		expiry: time.Now().Add(tokenCacheTTL),
	}
	elem := v.lru.PushFront(entry)
	v.index[key] = elem

	got, err := v.Validate(context.Background(), rawToken)
	if err != nil {
		t.Fatalf("Validate (cache hit): %v", err)
	}
	if got.Sub != want.Sub || got.Email != want.Email || got.UserID != want.UserID {
		t.Errorf("claims = %+v, want %+v", got, want)
	}
}

// TestEvictExpired_StopsOnContextCancel verifies that the background eviction
// goroutine exits cleanly when its context is cancelled.
func TestEvictExpired_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	v := &Validator{
		index: make(map[string]*list.Element),
		lru:   list.New(),
	}

	done := make(chan struct{})
	go func() {
		v.evictExpired(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// evictExpired returned after context cancellation — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("evictExpired did not stop after context was cancelled")
	}
}

// TestValidate_ExpiredCacheEntry_EagerEviction seeds a stale (already-expired)
// cache entry and verifies Validate removes it before attempting token verification.
// The nil verifier causes a panic on the verify call; we use recover() to catch it
// and then confirm the expired entry was removed from the index.
func TestValidate_ExpiredCacheEntry_EagerEviction(t *testing.T) {
	v := &Validator{
		index: make(map[string]*list.Element),
		lru:   list.New(),
	}

	rawToken := "stale-bearer-token"
	key := hashToken(rawToken)

	// Seed an already-expired entry.
	entry := &cachedEntry{
		key:    key,
		claims: &Claims{Sub: "old", Email: "old@test.com", UserID: "old"},
		expiry: time.Now().Add(-time.Hour),
	}
	elem := v.lru.PushFront(entry)
	v.index[key] = elem

	// Validate evicts the expired entry before calling v.verifier.Verify.
	// v.verifier is nil so Verify will panic; use recover to let the test continue.
	func() {
		defer func() { recover() }() //nolint:errcheck
		_, _ = v.Validate(context.Background(), rawToken)
	}()

	v.mu.Lock()
	_, stillPresent := v.index[key]
	v.mu.Unlock()

	if stillPresent {
		t.Error("expired cache entry should have been evicted before verifier call")
	}
	if v.lru.Len() != 0 {
		t.Errorf("LRU list len = %d, want 0 after eviction", v.lru.Len())
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

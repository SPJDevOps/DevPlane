package gateway

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
)

// ErrUnauthorized means the request is not authenticated (missing/invalid token).
var ErrUnauthorized = errors.New("unauthenticated")

// ErrForbidden means the caller presented a syntactically plausible token that is
// not permitted for this application (for example audience mismatch).
var ErrForbidden = errors.New("forbidden")

// OIDCConfig configures JWT validation against an OIDC issuer discovered at IssuerURL.
// ClientID is the OAuth2 client identifier used for the browser authorization-code flow.
// Audience is the expected JWT "aud" claim; when empty it defaults to ClientID.
type OIDCConfig struct {
	IssuerURL string
	ClientID  string
	Audience  string
}

const (
	tokenCacheTTL = 5 * time.Minute
	tokenCacheMax = 10_000 // maximum number of entries to prevent unbounded growth
)

// Claims holds verified identity extracted from an OIDC token.
type Claims struct {
	// Sub is the raw OIDC subject identifier.
	Sub string
	// Email is the user's email from the token claims.
	Email string
	// UserID is a Kubernetes-safe name derived from Sub (DNS label format).
	UserID string
}

// Validator verifies OIDC bearer tokens and caches results for tokenCacheTTL.
// The cache is bounded to tokenCacheMax entries using an LRU eviction policy so
// that a large number of distinct users cannot cause unbounded memory growth.
type Validator struct {
	verifier *gooidc.IDTokenVerifier
	mu       sync.Mutex
	index    map[string]*list.Element // hash → LRU list element
	lru      *list.List               // front = most recently used
}

type cachedEntry struct {
	key    string // hash of the raw token
	claims *Claims
	expiry time.Time
}

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeUserID converts an OIDC sub into a Kubernetes DNS-label-safe string.
// E.g. "user|12345" → "user-12345", truncated to 63 chars.
// Keycloak/UUID subs that start with a digit get a "u-" prefix so the result
// satisfies RFC 1035 (Service names must begin with a letter).
func sanitizeUserID(sub string) string {
	s := strings.ToLower(sub)
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		s = "u-" + s
	}
	// 49 = 63 (RFC 1035 DNS label max) − 14 ("-workspace-svc", the longest
	// resource-name suffix), so the derived Service name stays ≤ 63 chars.
	if len(s) > 49 {
		s = strings.TrimRight(s[:49], "-")
	}
	return s
}

// NewValidator creates a Validator that accepts tokens from issuerURL for clientID.
// It performs OIDC discovery to fetch the provider's JWKS endpoint.
// A background goroutine evicts expired cache entries every tokenCacheTTL.
func NewValidator(ctx context.Context, issuerURL, clientID string) (*Validator, error) {
	return NewValidatorWithOIDC(ctx, OIDCConfig{IssuerURL: issuerURL, ClientID: clientID})
}

// NewValidatorWithOIDC is like NewValidator but allows a distinct JWT audience (see OIDCConfig).
func NewValidatorWithOIDC(ctx context.Context, cfg OIDCConfig) (*Validator, error) {
	if cfg.IssuerURL == "" || cfg.ClientID == "" {
		return nil, fmt.Errorf("OIDC IssuerURL and ClientID are required")
	}
	audience := cfg.Audience
	if audience == "" {
		audience = cfg.ClientID
	}
	provider, err := gooidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC provider discovery %q: %w", cfg.IssuerURL, err)
	}
	v := &Validator{
		verifier: provider.Verifier(&gooidc.Config{ClientID: audience}),
		index:    make(map[string]*list.Element),
		lru:      list.New(),
	}
	go v.evictExpired(ctx)
	return v, nil
}

func classifyOIDCVerifyError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	// oidc reports audience mismatches as verification failures — treat as 403.
	if strings.Contains(msg, "aud") || strings.Contains(msg, "audience") {
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	return fmt.Errorf("%w: %v", ErrUnauthorized, err)
}

// evictExpired periodically removes expired entries from the token cache.
func (v *Validator) evictExpired(ctx context.Context) {
	ticker := time.NewTicker(tokenCacheTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			v.mu.Lock()
			for key, elem := range v.index {
				if now.After(elem.Value.(*cachedEntry).expiry) {
					v.lru.Remove(elem)
					delete(v.index, key)
				}
			}
			v.mu.Unlock()
		}
	}
}

// Validate verifies rawToken and returns the associated Claims.
// Valid tokens are cached for tokenCacheTTL to reduce IdP round-trips.
func (v *Validator) Validate(ctx context.Context, rawToken string) (*Claims, error) {
	key := hashToken(rawToken)

	v.mu.Lock()
	if elem, ok := v.index[key]; ok {
		entry := elem.Value.(*cachedEntry)
		if time.Now().Before(entry.expiry) {
			v.lru.MoveToFront(elem)
			claims := entry.claims
			v.mu.Unlock()
			return claims, nil
		}
		// Expired entry — evict eagerly rather than waiting for the background ticker.
		v.lru.Remove(elem)
		delete(v.index, key)
	}
	v.mu.Unlock()

	idToken, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, classifyOIDCVerifyError(err)
	}

	var raw struct {
		Email string `json:"email"`
	}
	if err := idToken.Claims(&raw); err != nil {
		return nil, fmt.Errorf("%w: extract claims: %v", ErrUnauthorized, err)
	}

	claims := &Claims{
		Sub:    idToken.Subject,
		Email:  raw.Email,
		UserID: sanitizeUserID(idToken.Subject),
	}

	v.mu.Lock()
	// Evict the LRU entry if we have reached the capacity limit.
	for v.lru.Len() >= tokenCacheMax {
		oldest := v.lru.Back()
		if oldest == nil {
			break
		}
		v.lru.Remove(oldest)
		delete(v.index, oldest.Value.(*cachedEntry).key)
	}
	entry := &cachedEntry{key: key, claims: claims, expiry: time.Now().Add(tokenCacheTTL)}
	elem := v.lru.PushFront(entry)
	v.index[key] = elem
	v.mu.Unlock()

	return claims, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

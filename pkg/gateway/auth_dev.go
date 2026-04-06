package gateway

import (
	"context"
	"fmt"
	"strings"
)

// FixedIdentityValidator accepts any non-empty bearer token and always returns the
// same claims. It must only be used for local development; it performs no signature
// or issuer checks.
type FixedIdentityValidator struct {
	claims *Claims
}

// NewFixedIdentityValidator returns a validator that maps every non-empty raw token
// to claims derived from sub and email (sub defaults to "dev-user" when empty).
func NewFixedIdentityValidator(sub, email string) *FixedIdentityValidator {
	if strings.TrimSpace(sub) == "" {
		sub = "dev-user"
	}
	return &FixedIdentityValidator{
		claims: &Claims{
			Sub:    sub,
			Email:  email,
			UserID: sanitizeUserID(sub),
		},
	}
}

// Validate implements the same contract as (*Validator).Validate.
func (f *FixedIdentityValidator) Validate(_ context.Context, rawToken string) (*Claims, error) {
	if strings.TrimSpace(rawToken) == "" {
		return nil, fmt.Errorf("%w: empty token", ErrUnauthorized)
	}
	return f.claims, nil
}

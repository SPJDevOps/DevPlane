package gateway

import (
	"testing"
)

func TestNewEndpointLimiter_DisabledReturnsNil(t *testing.T) {
	if NewEndpointLimiter(0, 0, 0, 0) != nil {
		t.Fatal("expected nil when both global and per-user limits are disabled")
	}
}

func TestEndpointLimiter_Allow_NilReceiver(t *testing.T) {
	var e *EndpointLimiter
	ok, scope := e.Allow("user-1")
	if !ok || scope != "" {
		t.Fatalf("Allow(nil) = %v, %q; want true, \"\"", ok, scope)
	}
}

func TestLoadEndpointLimiterFromEnv_DefaultsDisabled(t *testing.T) {
	t.Setenv("GATEWAY_RL_TEST_GLOBAL_RPS", "")
	t.Setenv("GATEWAY_RL_TEST_GLOBAL_BURST", "")
	t.Setenv("GATEWAY_RL_TEST_PER_USER_RPS", "")
	t.Setenv("GATEWAY_RL_TEST_PER_USER_BURST", "")
	if LoadEndpointLimiterFromEnv("GATEWAY_RL_TEST_") != nil {
		t.Fatal("expected nil limiter when env empty")
	}
}

func TestLoadEndpointLimiterFromEnv_PerUserOnly(t *testing.T) {
	t.Setenv("GATEWAY_RL_TEST_GLOBAL_RPS", "0")
	t.Setenv("GATEWAY_RL_TEST_GLOBAL_BURST", "0")
	t.Setenv("GATEWAY_RL_TEST_PER_USER_RPS", "5")
	t.Setenv("GATEWAY_RL_TEST_PER_USER_BURST", "10")
	lim := LoadEndpointLimiterFromEnv("GATEWAY_RL_TEST_")
	if lim == nil {
		t.Fatal("expected non-nil limiter")
	}
	ok, scope := lim.Allow("alice")
	if !ok || scope != "" {
		t.Fatalf("first Allow = %v %q", ok, scope)
	}
}

func TestEndpointLimiter_GlobalExhaustsBurst(t *testing.T) {
	// Global-only limiter: 1 token/s, burst 1 — second immediate Allow hits global.
	lim := NewEndpointLimiter(1, 1, 0, 0)
	if lim == nil {
		t.Fatal("expected limiter")
	}
	if ok, scope := lim.Allow("user-a"); !ok || scope != "" {
		t.Fatalf("first Allow = %v %q", ok, scope)
	}
	if ok, scope := lim.Allow("user-b"); ok || scope != "global" {
		t.Fatalf("second Allow = %v %q; want false, global", ok, scope)
	}
}

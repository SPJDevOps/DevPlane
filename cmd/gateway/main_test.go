package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	workspacev1alpha1 "workspace-operator/api/v1alpha1"
	gw "workspace-operator/pkg/gateway"
)

// --- stubs ---

type stubValidator struct {
	claims *gw.Claims
	err    error
}

func (v *stubValidator) Validate(_ context.Context, _ string) (*gw.Claims, error) {
	return v.claims, v.err
}

type stubLifecycle struct {
	ws  *workspacev1alpha1.Workspace
	err error
}

func (l *stubLifecycle) EnsureWorkspace(_ context.Context, _ string, _ *gw.Claims) (*workspacev1alpha1.Workspace, error) {
	return l.ws, l.err
}

func (l *stubLifecycle) TouchLastAccessed(_ context.Context, _ *workspacev1alpha1.Workspace) {}

type stubProxy struct {
	err error
}

func (p *stubProxy) ServeWS(w http.ResponseWriter, _ *http.Request, _ string, _ func()) error {
	// Simulate a successful upgrade by writing 101; real upgrades are tested in proxy_test.go.
	w.WriteHeader(http.StatusSwitchingProtocols)
	return p.err
}

// discardLog returns a no-op logger suitable for tests.
func discardLog() logr.Logger { return logr.Discard() }

// --- handleHealth tests ---

func TestHandleHealth(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	handleHealth(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if body := w.Body.String(); body != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

// --- envOr tests ---

func TestEnvOr_Present(t *testing.T) {
	t.Setenv("TEST_ENVOR_KEY", "myvalue")
	if got := envOr("TEST_ENVOR_KEY", "default"); got != "myvalue" {
		t.Errorf("envOr = %q, want myvalue", got)
	}
}

func TestEnvOr_Missing(t *testing.T) {
	if got := envOr("TEST_ENVOR_MISSING_XYZ", "fallback"); got != "fallback" {
		t.Errorf("envOr = %q, want fallback", got)
	}
}

// --- extractToken tests ---

func TestExtractToken_AuthHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Authorization", "Bearer mytoken")
	tok, err := extractToken(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "mytoken" {
		t.Errorf("token = %q, want %q", tok, "mytoken")
	}
}

func TestExtractToken_QueryParam(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws?token=qptoken", nil)
	tok, err := extractToken(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "qptoken" {
		t.Errorf("token = %q, want %q", tok, "qptoken")
	}
}

func TestExtractToken_Missing(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	_, err := extractToken(r)
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

// --- handleWS tests ---

func wsRequest(token string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/ws?token="+token, nil)
}

func TestMustEnv_Present(t *testing.T) {
	t.Setenv("TEST_MUSTENV_GATEWAY_KEY", "myvalue")
	got := mustEnv("TEST_MUSTENV_GATEWAY_KEY")
	if got != "myvalue" {
		t.Errorf("mustEnv = %q, want myvalue", got)
	}
}

func TestHandleWS_MissingToken(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ws", nil) // no token

	handleWS(w, r, &stubValidator{}, &stubLifecycle{}, &stubProxy{}, "default", discardLog())

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleWS_InvalidToken(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{err: errors.New("invalid token")}
	handleWS(w, wsRequest("badtoken"), v, &stubLifecycle{}, &stubProxy{}, "default", discardLog())

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleWS_WorkspaceProvisionFails(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: &gw.Claims{Sub: "u1", Email: "u1@test.com", UserID: "u1"}}
	lc := &stubLifecycle{err: errors.New("workspace failed")}
	handleWS(w, wsRequest("validtoken"), v, lc, &stubProxy{}, "default", discardLog())

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// TestHandleWS_StoppedWorkspaceRecovery verifies that when EnsureWorkspace
// succeeds (stopped workspace was cleared and re-provisioned by the lifecycle
// manager), the gateway proceeds to proxy rather than returning 500.
func TestHandleWS_StoppedWorkspaceRecovery(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: &gw.Claims{Sub: "u1", Email: "u1@test.com", UserID: "u1"}}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.ServiceEndpoint = "u1-workspace-svc.default.svc.cluster.local"
	// EnsureWorkspace succeeds (lifecycle manager internally restarted the stopped workspace).
	lc := &stubLifecycle{ws: ws}
	handleWS(w, wsRequest("validtoken"), v, lc, &stubProxy{}, "default", discardLog())

	// Expect the proxy to have been called (stub writes 101).
	if w.Code == http.StatusInternalServerError {
		t.Errorf("status = %d: handleWS returned an error instead of proxying", w.Code)
	}
}

func TestHandleWS_HappyPath(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: &gw.Claims{Sub: "u2", Email: "u2@test.com", UserID: "u2"}}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.ServiceEndpoint = "u2-workspace-svc.default.svc.cluster.local"
	lc := &stubLifecycle{ws: ws}
	handleWS(w, wsRequest("validtoken"), v, lc, &stubProxy{}, "default", discardLog())

	// stubProxy writes 101; no 4xx or 5xx from handleWS itself.
	if w.Code >= 400 {
		t.Errorf("status = %d, expected successful proxy", w.Code)
	}
}

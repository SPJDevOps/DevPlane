package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"golang.org/x/oauth2"
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

type stubOAuthConfig struct {
	authURL     string
	token       *oauth2.Token
	exchangeErr error
}

func (s *stubOAuthConfig) AuthCodeURL(state string, _ ...oauth2.AuthCodeOption) string {
	if s.authURL != "" {
		return s.authURL + "?state=" + state
	}
	return "https://idp.example.com/auth?state=" + state
}

func (s *stubOAuthConfig) Exchange(_ context.Context, _ string, _ ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	return s.token, s.exchangeErr
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

func TestExtractToken_Cookie(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "devplane_token", Value: "cookietoken"})
	tok, err := extractToken(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "cookietoken" {
		t.Errorf("token = %q, want cookietoken", tok)
	}
}

func TestExtractToken_HeaderWinsOverCookie(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer headertoken")
	r.AddCookie(&http.Cookie{Name: "devplane_token", Value: "cookietoken"})
	tok, err := extractToken(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "headertoken" {
		t.Errorf("token = %q, want headertoken (header wins over cookie)", tok)
	}
}

func TestExtractToken_CookieWinsOverQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?token=querytoken", nil)
	r.AddCookie(&http.Cookie{Name: "devplane_token", Value: "cookietoken"})
	tok, err := extractToken(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "cookietoken" {
		t.Errorf("token = %q, want cookietoken (cookie wins over query)", tok)
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

// --- handleLogin tests ---

func TestHandleLogin_SetsCookieAndRedirects(t *testing.T) {
	cfg := &stubOAuthConfig{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login", nil)

	handleLogin(w, r, cfg, false, discardLog())

	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}

	// Check state cookie is set with correct attributes.
	var stateCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "devplane_state" {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil {
		t.Fatal("devplane_state cookie not set")
	}
	if !stateCookie.HttpOnly {
		t.Error("state cookie should be HttpOnly")
	}
	if stateCookie.Secure {
		t.Error("state cookie should not be Secure for http")
	}
	if stateCookie.MaxAge != 600 {
		t.Errorf("state cookie MaxAge = %d, want 600", stateCookie.MaxAge)
	}
	if stateCookie.Value == "" {
		t.Error("state cookie value should not be empty")
	}

	// Check redirect URL contains the state.
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "state="+stateCookie.Value) {
		t.Errorf("redirect URL %q does not contain state=%s", loc, stateCookie.Value)
	}
}

func TestHandleLogin_SecureCookie(t *testing.T) {
	cfg := &stubOAuthConfig{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login", nil)

	handleLogin(w, r, cfg, true, discardLog())

	resp := w.Result()
	for _, c := range resp.Cookies() {
		if c.Name == "devplane_state" && !c.Secure {
			t.Error("state cookie should be Secure=true when cookieSecure=true")
		}
	}
}

// --- handleCallback tests ---

func TestHandleCallback_MissingStateCookie(t *testing.T) {
	cfg := &stubOAuthConfig{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/callback?state=abc&code=xyz", nil)

	handleCallback(w, r, cfg, &stubValidator{}, false, discardLog())

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCallback_StateMismatch(t *testing.T) {
	cfg := &stubOAuthConfig{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/callback?state=wrong&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: "devplane_state", Value: "correct"})

	handleCallback(w, r, cfg, &stubValidator{}, false, discardLog())

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCallback_ExchangeError(t *testing.T) {
	cfg := &stubOAuthConfig{exchangeErr: errors.New("exchange failed")}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/callback?state=mystate&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: "devplane_state", Value: "mystate"})

	handleCallback(w, r, cfg, &stubValidator{}, false, discardLog())

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestHandleCallback_MissingIDToken(t *testing.T) {
	// Token without id_token extra field.
	cfg := &stubOAuthConfig{token: &oauth2.Token{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/callback?state=mystate&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: "devplane_state", Value: "mystate"})

	handleCallback(w, r, cfg, &stubValidator{}, false, discardLog())

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestHandleCallback_InvalidToken(t *testing.T) {
	tok := (&oauth2.Token{}).WithExtra(map[string]interface{}{"id_token": "rawtoken"})
	cfg := &stubOAuthConfig{token: tok}
	v := &stubValidator{err: errors.New("invalid")}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/callback?state=mystate&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: "devplane_state", Value: "mystate"})

	handleCallback(w, r, cfg, v, false, discardLog())

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleCallback_HappyPath(t *testing.T) {
	tok := (&oauth2.Token{}).WithExtra(map[string]interface{}{"id_token": "validtoken"})
	cfg := &stubOAuthConfig{token: tok}
	v := &stubValidator{claims: &gw.Claims{Sub: "u1", Email: "u1@example.com", UserID: "u1"}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/callback?state=mystate&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: "devplane_state", Value: "mystate"})

	handleCallback(w, r, cfg, v, false, discardLog())

	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Errorf("redirect location = %q, want /", loc)
	}

	var tokenCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "devplane_token" {
			tokenCookie = c
			break
		}
	}
	if tokenCookie == nil {
		t.Fatal("devplane_token cookie not set")
	}
	if tokenCookie.Value != "validtoken" {
		t.Errorf("devplane_token = %q, want validtoken", tokenCookie.Value)
	}
	if !tokenCookie.HttpOnly {
		t.Error("devplane_token cookie should be HttpOnly")
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

// --- handleProxy tests ---

func TestHandleProxy_NoToken_RedirectsToLogin(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	handleProxy(w, r, &stubValidator{}, &stubLifecycle{}, "default", false, discardLog())

	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("redirect location = %q, want /login", loc)
	}
}

func TestHandleProxy_InvalidToken_RedirectsToLogin(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "devplane_token", Value: "staletoken"})

	v := &stubValidator{err: errors.New("expired")}
	handleProxy(w, r, v, &stubLifecycle{}, "default", false, discardLog())

	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("redirect location = %q, want /login", loc)
	}

	// Stale cookie should be cleared.
	for _, c := range resp.Cookies() {
		if c.Name == "devplane_token" && c.MaxAge != -1 {
			t.Errorf("stale devplane_token cookie MaxAge = %d, want -1 (cleared)", c.MaxAge)
		}
	}
}

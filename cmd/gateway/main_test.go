package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	ws        *workspacev1alpha1.Workspace
	err       error
	existsWs  *workspacev1alpha1.Workspace
	existsErr error
}

func (l *stubLifecycle) EnsureExists(_ context.Context, _ string, _ *gw.Claims) (*workspacev1alpha1.Workspace, gw.EnsureDetails, error) {
	return l.existsWs, gw.EnsureDetails{}, l.existsErr
}

func (l *stubLifecycle) EnsureWorkspace(_ context.Context, _ string, _ *gw.Claims) (*workspacev1alpha1.Workspace, gw.EnsureDetails, error) {
	return l.ws, gw.EnsureDetails{}, l.err
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

func TestParseOIDCClockSkew_Default(t *testing.T) {
	t.Setenv("OIDC_CLOCK_SKEW", "")
	d, err := parseOIDCClockSkew()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d != 60*time.Second {
		t.Fatalf("default = %v, want 60s", d)
	}
}

func TestParseOIDCClockSkew_Zero(t *testing.T) {
	t.Setenv("OIDC_CLOCK_SKEW", "0")
	d, err := parseOIDCClockSkew()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if d != 0 {
		t.Fatalf("d = %v, want 0", d)
	}
}

func TestParseOIDCClockSkew_Invalid(t *testing.T) {
	t.Setenv("OIDC_CLOCK_SKEW", "not-a-duration")
	_, err := parseOIDCClockSkew()
	if err == nil {
		t.Fatal("expected error")
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

	handleWS(w, r, &stubValidator{}, &stubLifecycle{}, &stubProxy{}, "default", discardLog(), nil)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.AuthErrorCodeUnauthorized {
		t.Errorf("error = %q, want %q", body["error"], gw.AuthErrorCodeUnauthorized)
	}
}

func TestHandleWS_InvalidToken(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{err: fmt.Errorf("%w: invalid", gw.ErrUnauthorized)}
	handleWS(w, wsRequest("badtoken"), v, &stubLifecycle{}, &stubProxy{}, "default", discardLog(), nil)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.AuthErrorCodeUnauthorized {
		t.Errorf("error = %q, want unauthorized", body["error"])
	}
}

func TestHandleWS_ForbiddenAudience(t *testing.T) {
	w := httptest.NewRecorder()
	v := &stubValidator{err: fmt.Errorf("%w: aud", gw.ErrForbidden)}
	handleWS(w, wsRequest("tok"), v, &stubLifecycle{}, &stubProxy{}, "default", discardLog(), nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.AuthErrorCodeForbidden {
		t.Errorf("error = %q, want forbidden", body["error"])
	}
}

func TestHandleWS_TokenExpired(t *testing.T) {
	w := httptest.NewRecorder()
	v := &stubValidator{err: fmt.Errorf("%w: expired", gw.ErrTokenExpired)}
	handleWS(w, wsRequest("tok"), v, &stubLifecycle{}, &stubProxy{}, "default", discardLog(), nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.AuthErrorCodeTokenExpired {
		t.Errorf("error = %q, want token_expired", body["error"])
	}
}

func TestHandleWS_WorkspaceProvisionFails(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: &gw.Claims{Sub: "u1", Email: "u1@test.com", UserID: "u1"}}
	lc := &stubLifecycle{err: errors.New("workspace failed")}
	handleWS(w, wsRequest("validtoken"), v, lc, &stubProxy{}, "default", discardLog(), nil)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.WorkspaceErrorCodeUnavailable {
		t.Errorf("error = %q, want %q", body["error"], gw.WorkspaceErrorCodeUnavailable)
	}
}

// TestHandleWS_StoppedWorkspaceRecovery verifies that when EnsureWorkspace
// succeeds (stopped workspace was cleared and re-provisioned by the lifecycle
// manager), the gateway proceeds to proxy rather than returning 500.
func TestHandleWS_StoppedWorkspaceRecovery(t *testing.T) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", 7681))
	if err != nil {
		t.Skipf("cannot bind to port 7681 (likely in use): %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	w := httptest.NewRecorder()

	v := &stubValidator{claims: &gw.Claims{Sub: "u1", Email: "u1@test.com", UserID: "u1"}}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.ServiceEndpoint = "127.0.0.1"
	// EnsureWorkspace succeeds (lifecycle manager internally restarted the stopped workspace).
	lc := &stubLifecycle{ws: ws}
	handleWS(w, wsRequest("validtoken"), v, lc, &stubProxy{}, "default", discardLog(), nil)

	// Expect the proxy to have been called (stub writes 101).
	if w.Code == http.StatusInternalServerError {
		t.Errorf("status = %d: handleWS returned an error instead of proxying", w.Code)
	}
}

func TestHandleWS_HappyPath(t *testing.T) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", 7681))
	if err != nil {
		t.Skipf("cannot bind to port 7681 (likely in use): %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	w := httptest.NewRecorder()

	v := &stubValidator{claims: &gw.Claims{Sub: "u2", Email: "u2@test.com", UserID: "u2"}}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.ServiceEndpoint = "127.0.0.1"
	lc := &stubLifecycle{ws: ws}
	handleWS(w, wsRequest("validtoken"), v, lc, &stubProxy{}, "default", discardLog(), nil)

	// stubProxy writes 101; no 4xx or 5xx from handleWS itself.
	if w.Code >= 400 {
		t.Errorf("status = %d, expected successful proxy", w.Code)
	}
}

func TestHandleWS_BackendNotReady_Returns503(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: &gw.Claims{Sub: "u3", Email: "u3@test.com", UserID: "u3"}}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	// 127.0.0.1:7681 not listening → BackendReady returns false immediately.
	ws.Status.ServiceEndpoint = "127.0.0.1"
	lc := &stubLifecycle{ws: ws}
	handleWS(w, wsRequest("validtoken"), v, lc, &stubProxy{}, "default", discardLog(), nil)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.WorkspaceErrorCodeNotReady {
		t.Errorf("error = %q, want %q", body["error"], gw.WorkspaceErrorCodeNotReady)
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

func proxyRequest(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "devplane_token", Value: token})
	return r
}

func validClaims() *gw.Claims {
	return &gw.Claims{Sub: "alice", Email: "alice@example.com", UserID: "alice"}
}

func TestHandleProxy_EnsureExistsFails(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: validClaims()}
	lc := &stubLifecycle{existsErr: errors.New("k8s unavailable")}
	handleProxy(w, proxyRequest("tok"), v, lc, "default", false, discardLog())

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandleProxy_PendingPhase_ServesLoadingPage(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: validClaims()}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhasePending
	lc := &stubLifecycle{existsWs: ws}
	handleProxy(w, proxyRequest("tok"), v, lc, "default", false, discardLog())

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("loading page missing meta-refresh")
	}
	if !strings.Contains(body, "alice@example.com") {
		t.Error("loading page missing user email")
	}
	if !strings.Contains(body, "Pending") {
		t.Error("loading page missing phase Pending")
	}
}

func TestHandleProxy_CreatingPhase_ServesLoadingPage(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: validClaims()}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseCreating
	lc := &stubLifecycle{existsWs: ws}
	handleProxy(w, proxyRequest("tok"), v, lc, "default", false, discardLog())

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Creating") {
		t.Error("loading page missing phase Creating")
	}
}

func TestHandleProxy_RunningNoEndpoint_ServesLoadingPage(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: validClaims()}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.ServiceEndpoint = "" // endpoint not yet set
	lc := &stubLifecycle{existsWs: ws}
	handleProxy(w, proxyRequest("tok"), v, lc, "default", false, discardLog())

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `http-equiv="refresh"`) {
		t.Error("loading page missing meta-refresh")
	}
}

func TestHandleProxy_FreshWorkspace_ServesLoadingPage(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: validClaims()}
	ws := &workspacev1alpha1.Workspace{} // phase == "" (brand new CR)
	lc := &stubLifecycle{existsWs: ws}
	handleProxy(w, proxyRequest("tok"), v, lc, "default", false, discardLog())

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `http-equiv="refresh"`) {
		t.Error("loading page missing meta-refresh")
	}
}

func TestHandleProxy_RunningWithEndpoint_ErrorHandlerServesLoadingPage(t *testing.T) {
	w := httptest.NewRecorder()

	v := &stubValidator{claims: validClaims()}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	// ServiceEndpoint is a bare hostname; BackendHTTPURL appends :7681.
	// 127.0.0.1 → http://127.0.0.1:7681 — connection refused immediately (no ttyd in tests).
	ws.Status.ServiceEndpoint = "127.0.0.1"
	lc := &stubLifecycle{existsWs: ws}
	handleProxy(w, proxyRequest("tok"), v, lc, "default", false, discardLog())

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (ErrorHandler should serve loading page)", w.Code)
	}
	if !strings.Contains(w.Body.String(), `http-equiv="refresh"`) {
		t.Error("loading page missing meta-refresh")
	}
}

func TestHandleProxy_EmailFallsBackToUserID(t *testing.T) {
	w := httptest.NewRecorder()

	claims := &gw.Claims{Sub: "bob", Email: "", UserID: "bob"}
	v := &stubValidator{claims: claims}
	ws := &workspacev1alpha1.Workspace{}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhasePending
	lc := &stubLifecycle{existsWs: ws}
	handleProxy(w, proxyRequest("tok"), v, lc, "default", false, discardLog())

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "bob") {
		t.Error("loading page should contain UserID when email is empty")
	}
}

func TestServeLoadingPage_HTMLEscapesDisplayName(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	serveLoadingPage(w, r, "<script>alert(1)</script>", "Pending")

	body := w.Body.String()
	if strings.Contains(body, "<script>") {
		t.Error("XSS: raw <script> tag found in loading page output")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected HTML-escaped &lt;script&gt; in output")
	}
}

func TestServeLoadingPage_Status200(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	serveLoadingPage(w, r, "alice", "Creating")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

// --- handleWorkspaceAPI tests ---

func TestHandleWorkspaceAPI_MethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/workspace", nil)
	handleWorkspaceAPI(w, r, &stubValidator{}, &stubLifecycle{}, "default", false, discardLog(), nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleWorkspaceAPI_POST_OK(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/workspace", nil)
	r.Header.Set("Authorization", "Bearer tok")
	v := &stubValidator{claims: validClaims()}
	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "default"},
	}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhasePending
	lc := &stubLifecycle{existsWs: ws}
	handleWorkspaceAPI(w, r, v, lc, "default", false, discardLog(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got workspaceAPIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if got.Name != "alice" {
		t.Errorf("name = %q, want alice", got.Name)
	}
}

func TestHandleWorkspaceAPI_UnauthorizedNoToken(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	handleWorkspaceAPI(w, r, &stubValidator{}, &stubLifecycle{}, "default", false, discardLog(), nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != "unauthorized" {
		t.Errorf("error = %q, want unauthorized", body["error"])
	}
}

func TestHandleWorkspaceAPI_InvalidTokenJSON(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	r.Header.Set("Authorization", "Bearer bad")
	v := &stubValidator{err: fmt.Errorf("%w: invalid", gw.ErrUnauthorized)}
	handleWorkspaceAPI(w, r, v, &stubLifecycle{}, "default", false, discardLog(), nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleWorkspaceAPI_ForbiddenJSON(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	r.Header.Set("Authorization", "Bearer tok")
	v := &stubValidator{err: fmt.Errorf("%w: aud mismatch", gw.ErrForbidden)}
	handleWorkspaceAPI(w, r, v, &stubLifecycle{}, "default", false, discardLog(), nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.AuthErrorCodeForbidden {
		t.Errorf("error = %q, want forbidden", body["error"])
	}
}

func TestHandleWorkspaceAPI_EnsureExistsFails(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	r.Header.Set("Authorization", "Bearer tok")
	v := &stubValidator{claims: validClaims()}
	lc := &stubLifecycle{existsErr: errors.New("k8s down")}
	handleWorkspaceAPI(w, r, v, lc, "default", false, discardLog(), nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandleWorkspaceAPI_OK(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	r.Header.Set("Authorization", "Bearer tok")
	v := &stubValidator{claims: validClaims()}
	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "default"},
	}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhasePending
	ws.Status.Message = "waiting"
	lc := &stubLifecycle{existsWs: ws}
	handleWorkspaceAPI(w, r, v, lc, "default", false, discardLog(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got workspaceAPIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if got.Name != "alice" || got.Namespace != "default" {
		t.Errorf("name/ns = %q/%q, want alice/default", got.Name, got.Namespace)
	}
	if got.Phase != "Pending" {
		t.Errorf("phase = %q, want Pending", got.Phase)
	}
	if got.Message != "waiting" {
		t.Errorf("message = %q, want waiting", got.Message)
	}
	if got.TTYDReady {
		t.Error("TTYDReady should be false for Pending workspace")
	}
}

func TestHandleWorkspaceAPI_TTYDReadyTrue(t *testing.T) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", 7681))
	if err != nil {
		t.Skipf("cannot bind to port 7681: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	r.Header.Set("Authorization", "Bearer tok")
	v := &stubValidator{claims: validClaims()}
	ws := &workspacev1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "default"},
	}
	ws.Status.Phase = workspacev1alpha1.WorkspacePhaseRunning
	ws.Status.ServiceEndpoint = "127.0.0.1"
	lc := &stubLifecycle{existsWs: ws}
	handleWorkspaceAPI(w, r, v, lc, "default", false, discardLog(), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got workspaceAPIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if !got.TTYDReady {
		t.Error("TTYDReady = false, want true when backend accepts TCP")
	}
}

// TestHandleWorkspaceAPI_RateLimited verifies the lifecycle JSON API returns HTTP 429 with
// {"error":"rate_limited"} once the configured (non-nil) limiter rejects a request after OIDC
// validation. Uses a global token bucket (1 RPS, burst 1): first request passes the limiter
// then fails downstream; second hits the limiter first.
func TestHandleWorkspaceAPI_RateLimited(t *testing.T) {
	rl := gw.NewEndpointLimiter(1, 1, 0, 0)
	v := &stubValidator{claims: validClaims()}
	lc := &stubLifecycle{existsErr: errors.New("downstream")}

	r1 := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	r1.Header.Set("Authorization", "Bearer tok")
	w1 := httptest.NewRecorder()
	handleWorkspaceAPI(w1, r1, v, lc, "default", false, discardLog(), rl)
	if w1.Code != http.StatusInternalServerError {
		t.Fatalf("first request status = %d, want 500 (past rate limit, downstream error)", w1.Code)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	r2.Header.Set("Authorization", "Bearer tok")
	w2 := httptest.NewRecorder()
	handleWorkspaceAPI(w2, r2, v, lc, "default", false, discardLog(), rl)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", w2.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.RateLimitErrorCode {
		t.Errorf("error = %q, want %q", body["error"], gw.RateLimitErrorCode)
	}
	if w2.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header on rate-limited response")
	}
}

// TestHandleWorkspaceAPI_RateLimitedPerUser exercises per-identity limits only (no global bucket),
// matching a typical prod tune such as perUserRPS=2 and perUserBurst=3. Burst 3 allows three
// immediate Allows; the fourth hits the limiter with scope "user" before EnsureExists.
func TestHandleWorkspaceAPI_RateLimitedPerUser(t *testing.T) {
	rl := gw.NewEndpointLimiter(0, 0, 2, 3)
	v := &stubValidator{claims: validClaims()}
	lc := &stubLifecycle{existsErr: errors.New("downstream")}

	before := gw.RateLimitHitsTotal("lifecycle", "user")
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
		r.Header.Set("Authorization", "Bearer tok")
		handleWorkspaceAPI(w, r, v, lc, "default", false, discardLog(), rl)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("request %d status = %d, want 500 (past rate limit, downstream error)", i+1, w.Code)
		}
	}

	w4 := httptest.NewRecorder()
	r4 := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	r4.Header.Set("Authorization", "Bearer tok")
	handleWorkspaceAPI(w4, r4, v, lc, "default", false, discardLog(), rl)
	if w4.Code != http.StatusTooManyRequests {
		t.Fatalf("fourth request status = %d, want 429", w4.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w4.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.RateLimitErrorCode {
		t.Errorf("error = %q, want %q", body["error"], gw.RateLimitErrorCode)
	}
	if gw.RateLimitHitsTotal("lifecycle", "user") != before+1 {
		t.Errorf("expected exactly one new lifecycle/user rate-limit hit")
	}
}

// TestHandleWS_RateLimited verifies the WebSocket handler returns JSON 429 before upgrade when
// the WebSocket connect limiter trips (same global 1/1 pattern as TestHandleWorkspaceAPI_RateLimited).
func TestHandleWS_RateLimited(t *testing.T) {
	rl := gw.NewEndpointLimiter(1, 1, 0, 0)
	v := &stubValidator{claims: &gw.Claims{Sub: "u1", Email: "u1@test.com", UserID: "u1"}}
	lc := &stubLifecycle{err: errors.New("downstream")}

	w1 := httptest.NewRecorder()
	handleWS(w1, wsRequest("a"), v, lc, &stubProxy{}, "default", discardLog(), rl)
	if w1.Code != http.StatusInternalServerError {
		t.Fatalf("first request status = %d, want 500", w1.Code)
	}

	w2 := httptest.NewRecorder()
	handleWS(w2, wsRequest("a"), v, lc, &stubProxy{}, "default", discardLog(), rl)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", w2.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.RateLimitErrorCode {
		t.Errorf("error = %q, want %q", body["error"], gw.RateLimitErrorCode)
	}
	if w2.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header on rate-limited response")
	}
}

// TestHandleWS_RateLimitedPerUser mirrors TestHandleWorkspaceAPI_RateLimitedPerUser for the
// WebSocket connect path (HTTP response before upgrade).
func TestHandleWS_RateLimitedPerUser(t *testing.T) {
	rl := gw.NewEndpointLimiter(0, 0, 2, 3)
	v := &stubValidator{claims: &gw.Claims{Sub: "u1", Email: "u1@test.com", UserID: "u1"}}
	lc := &stubLifecycle{err: errors.New("downstream")}

	before := gw.RateLimitHitsTotal("websocket", "user")
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		handleWS(w, wsRequest("a"), v, lc, &stubProxy{}, "default", discardLog(), rl)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("request %d status = %d, want 500", i+1, w.Code)
		}
	}

	w4 := httptest.NewRecorder()
	handleWS(w4, wsRequest("a"), v, lc, &stubProxy{}, "default", discardLog(), rl)
	if w4.Code != http.StatusTooManyRequests {
		t.Fatalf("fourth request status = %d, want 429", w4.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w4.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != gw.RateLimitErrorCode {
		t.Errorf("error = %q, want %q", body["error"], gw.RateLimitErrorCode)
	}
	if gw.RateLimitHitsTotal("websocket", "user") != before+1 {
		t.Errorf("expected exactly one new websocket/user rate-limit hit")
	}
}

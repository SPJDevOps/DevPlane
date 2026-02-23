// Package main is the entrypoint for the workspace gateway service.
// It validates OIDC tokens, creates/retrieves Workspace CRs, and proxies
// WebSocket connections from authenticated users to their workspace pods.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
	gw "workspace-operator/pkg/gateway"
)

var scheme = runtime.NewScheme()

// tokenValidator verifies an OIDC bearer token and returns checked claims.
type tokenValidator interface {
	Validate(ctx context.Context, rawToken string) (*gw.Claims, error)
}

// workspaceLifecycle creates or retrieves the user's workspace and tracks activity.
type workspaceLifecycle interface {
	EnsureWorkspace(ctx context.Context, namespace string, claims *gw.Claims) (*workspacev1alpha1.Workspace, error)
	TouchLastAccessed(ctx context.Context, ws *workspacev1alpha1.Workspace)
}

// wsProxy proxies a WebSocket connection to a backend URL.
type wsProxy interface {
	ServeWS(w http.ResponseWriter, r *http.Request, backendURL string, onActivity func()) error
}

// oauthConfig abstracts *oauth2.Config for testability.
type oauthConfig interface {
	AuthCodeURL(state string, opts ...oauth2.AuthCodeOption) string
	Exchange(ctx context.Context, code string, opts ...oauth2.AuthCodeOption) (*oauth2.Token, error)
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(workspacev1alpha1.AddToScheme(scheme))
}

func main() {
	zapLog, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init logger: %v\n", err)
		os.Exit(1)
	}
	log := zapr.NewLogger(zapLog)

	issuerURL := mustEnv("OIDC_ISSUER_URL")
	clientID := mustEnv("OIDC_CLIENT_ID")
	clientSecret := mustEnv("OIDC_CLIENT_SECRET")
	redirectURL := mustEnv("OIDC_REDIRECT_URL")
	namespace := envOr("NAMESPACE", "default")
	port := envOr("PORT", "8080")
	aiProvidersJSON := envOr("AI_PROVIDERS_JSON",
		`[{"name":"local","endpoint":"http://vllm.ai-system.svc:8000","models":["deepseek-coder-33b-instruct"]}]`)
	var aiProviders []workspacev1alpha1.AIProvider
	if err := json.Unmarshal([]byte(aiProvidersJSON), &aiProviders); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse AI_PROVIDERS_JSON: %v\n", err)
		os.Exit(1)
	}

	cookieSecure := strings.HasPrefix(redirectURL, "https://")

	ctx := ctrl.SetupSignalHandler()

	validator, err := gw.NewValidator(ctx, issuerURL, clientID)
	if err != nil {
		log.Error(err, "Failed to initialize OIDC validator")
		os.Exit(1)
	}
	log.Info("OIDC validator ready", "issuer", issuerURL)

	oidcProvider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		log.Error(err, "Failed to initialize OIDC provider for OAuth2 flow")
		os.Exit(1)
	}
	oauth2Cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     oidcProvider.Endpoint(),
		Scopes:       []string{gooidc.ScopeOpenID, "email", "profile"},
	}

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "Failed to get Kubernetes config")
		os.Exit(1)
	}
	k8sClient, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Failed to create Kubernetes client")
		os.Exit(1)
	}

	lifecycle := gw.NewLifecycleManager(k8sClient, log, gw.LifecycleConfig{
		Providers:      aiProviders,
		DefaultCPU:     "2",
		DefaultMemory:  "4Gi",
		DefaultStorage: "20Gi",
	})
	proxy := gw.NewProxy(log)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWS(w, r, validator, lifecycle, proxy, namespace, log)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		handleLogin(w, r, oauth2Cfg, cookieSecure, log)
	})
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		handleCallback(w, r, oauth2Cfg, validator, cookieSecure, log)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleProxy(w, r, validator, lifecycle, namespace, cookieSecure, log)
	})

	srv := &http.Server{
		Addr:        ":" + port,
		Handler:     mux,
		ReadTimeout: 30 * time.Second,
		// No write timeout: WebSocket connections are long-lived.
	}
	log.Info("Gateway listening", "addr", srv.Addr, "namespace", namespace)

	srvErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			srvErr <- err
		}
		close(srvErr)
	}()

	select {
	case <-ctx.Done():
		log.Info("Shutting down gateway server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error(err, "Server shutdown error")
		}
	case err := <-srvErr:
		if err != nil {
			log.Error(err, "Server failed")
			os.Exit(1)
		}
	}
}

// handleHealth responds to liveness and readiness probes.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleLogin initiates the OIDC authorization code flow by setting a CSRF
// state cookie and redirecting the browser to the identity provider.
func handleLogin(w http.ResponseWriter, r *http.Request, cfg oauthConfig, secure bool, log logr.Logger) {
	state := uuid.NewString()
	http.SetCookie(w, &http.Cookie{
		Name:     "devplane_state",
		Value:    state,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	log.Info("Redirecting to IdP", "remote", r.RemoteAddr)
	http.Redirect(w, r, cfg.AuthCodeURL(state), http.StatusFound)
}

// handleCallback completes the OIDC authorization code flow: exchanges the
// code for tokens, validates the ID token, sets a session cookie, and
// redirects the browser to the root path.
func handleCallback(w http.ResponseWriter, r *http.Request,
	cfg oauthConfig, validator tokenValidator, secure bool, log logr.Logger,
) {
	stateCookie, err := r.Cookie("devplane_state")
	if err != nil {
		http.Error(w, "Missing state cookie", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "State mismatch", http.StatusBadRequest)
		return
	}

	// Clear the state cookie immediately after validation.
	http.SetCookie(w, &http.Cookie{
		Name:     "devplane_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
	})

	token, err := cfg.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		log.Error(err, "Token exchange failed")
		http.Error(w, "Token exchange failed", http.StatusBadGateway)
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		http.Error(w, "Missing id_token in token response", http.StatusBadGateway)
		return
	}

	if _, err := validator.Validate(r.Context(), rawIDToken); err != nil {
		http.Error(w, "Invalid ID token", http.StatusUnauthorized)
		return
	}

	expiry := token.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(time.Hour)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "devplane_token",
		Value:    rawIDToken,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/", http.StatusFound)
}

// handleProxy is the catch-all handler that proxies authenticated HTTP
// requests (e.g. the ttyd web UI) to the user's workspace pod.
// Unauthenticated requests are redirected to /login.
func handleProxy(w http.ResponseWriter, r *http.Request,
	validator tokenValidator, lifecycle workspaceLifecycle,
	namespace string, secure bool, log logr.Logger,
) {
	rawToken, err := extractToken(r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	claims, err := validator.Validate(r.Context(), rawToken)
	if err != nil {
		// Clear stale cookie then redirect to login.
		http.SetCookie(w, &http.Cookie{
			Name:     "devplane_token",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   secure,
		})
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	ws, err := lifecycle.EnsureWorkspace(r.Context(), namespace, claims)
	if err != nil {
		http.Error(w, "Failed to provision workspace", http.StatusInternalServerError)
		log.Error(err, "EnsureWorkspace failed", "user", claims.UserID)
		return
	}

	target, _ := url.Parse(gw.BackendHTTPURL(ws.Status.ServiceEndpoint))
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ServeHTTP(w, r)
}

// handleWS is the main WebSocket endpoint. It validates the caller's OIDC token,
// provisions or retrieves their Workspace CR, then proxies the connection to the
// workspace pod's ttyd server.
func handleWS(w http.ResponseWriter, r *http.Request,
	validator tokenValidator,
	lifecycle workspaceLifecycle,
	proxy wsProxy,
	namespace string,
	log logr.Logger,
) {
	rawToken, err := extractToken(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		log.Info("Missing token", "remote", r.RemoteAddr)
		return
	}

	claims, err := validator.Validate(r.Context(), rawToken)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		log.Info("Invalid token", "remote", r.RemoteAddr, "error", err.Error())
		return
	}

	ws, err := lifecycle.EnsureWorkspace(r.Context(), namespace, claims)
	if err != nil {
		http.Error(w, "Failed to provision workspace", http.StatusInternalServerError)
		log.Error(err, "EnsureWorkspace failed", "user", claims.UserID)
		return
	}

	backendURL := gw.BackendURL(ws.Status.ServiceEndpoint)
	log.Info("Proxying WebSocket", "user", claims.UserID, "backend", backendURL)

	// Rate-limited activity callback: update LastAccessed at most once per minute
	// so the idle-timeout controller sees genuine activity, not the initial timestamp.
	var lastTouch time.Time
	onActivity := func() {
		if time.Since(lastTouch) < time.Minute {
			return
		}
		lastTouch = time.Now()
		lifecycle.TouchLastAccessed(r.Context(), ws)
	}

	if err := proxy.ServeWS(w, r, backendURL, onActivity); err != nil {
		log.Info("WebSocket session ended", "user", claims.UserID, "reason", err.Error())
	}
}

// extractToken returns the bearer token from the Authorization header, the
// devplane_token cookie, or the ?token query parameter (in that priority order).
// The cookie is used by the browser login flow; the query parameter is needed
// because the browser WebSocket API does not support custom request headers.
func extractToken(r *http.Request) (string, error) {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer "), nil
	}
	if c, err := r.Cookie("devplane_token"); err == nil && c.Value != "" {
		return c.Value, nil
	}
	if token := r.URL.Query().Get("token"); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("no token in Authorization header, devplane_token cookie, or ?token query param")
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "required env var %q is not set\n", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

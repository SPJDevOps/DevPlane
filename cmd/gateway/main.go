// Package main is the entrypoint for the workspace gateway service.
// It validates OIDC tokens, creates/retrieves Workspace CRs, and proxies
// WebSocket connections from authenticated users to their workspace pods.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
	gw "workspace-operator/pkg/gateway"
)

var scheme = runtime.NewScheme()

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
	namespace := envOr("NAMESPACE", "default")
	port := envOr("PORT", "8080")
	aiProvidersJSON := envOr("AI_PROVIDERS_JSON",
		`[{"name":"local","endpoint":"http://vllm.ai-system.svc:8000","models":["deepseek-coder-33b-instruct"]}]`)
	var aiProviders []workspacev1alpha1.AIProvider
	if err := json.Unmarshal([]byte(aiProvidersJSON), &aiProviders); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse AI_PROVIDERS_JSON: %v\n", err)
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	validator, err := gw.NewValidator(ctx, issuerURL, clientID)
	if err != nil {
		log.Error(err, "Failed to initialize OIDC validator")
		os.Exit(1)
	}
	log.Info("OIDC validator ready", "issuer", issuerURL)

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

	srv := &http.Server{
		Addr:        ":" + port,
		Handler:     mux,
		ReadTimeout: 30 * time.Second,
		// No write timeout: WebSocket connections are long-lived.
	}
	log.Info("Gateway listening", "addr", srv.Addr, "namespace", namespace)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error(err, "Server failed")
		os.Exit(1)
	}
}

// handleHealth responds to liveness and readiness probes.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleWS is the main WebSocket endpoint. It validates the caller's OIDC token,
// provisions or retrieves their Workspace CR, then proxies the connection to the
// workspace pod's ttyd server.
func handleWS(w http.ResponseWriter, r *http.Request,
	validator *gw.Validator,
	lifecycle *gw.LifecycleManager,
	proxy *gw.Proxy,
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

	if err := proxy.ServeWS(w, r, backendURL); err != nil {
		log.Info("WebSocket session ended", "user", claims.UserID, "reason", err.Error())
	}
}

// extractToken returns the Bearer token from either the Authorization header or
// the ?token query parameter. The query parameter is needed because the browser
// WebSocket API does not support custom request headers.
func extractToken(r *http.Request) (string, error) {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer "), nil
	}
	if token := r.URL.Query().Get("token"); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("no token in Authorization header or ?token query param")
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

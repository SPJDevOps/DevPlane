package gateway

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	// Log keys for gateway structured logs (match operator conventions where possible).
	LogKeyComponent = "devplane.component"
	LogKeyEvent     = "devplane.event"
	LogKeyUserID    = "userId"
	LogKeyNamespace = "namespace"
	LogKeyWorkspace = "workspace"
)

// Gateway component value for structured logs.
const ComponentGateway = "gateway"

// Event names for gateway logs.
const (
	EventAuthFailure            = "gateway.auth.failure"
	EventWorkspaceError         = "gateway.workspace.error"
	EventOIDCTokenExchange      = "gateway.oidc.token_exchange.failure"
	EventOIDCInvalidIDToken     = "gateway.oidc.id_token.invalid"
	EventWSProxyStart           = "gateway.ws.proxy.start"
	EventWSProxyBackendNotReady = "gateway.ws.backend_not_ready"
	EventWSProxySessionEnd      = "gateway.ws.session.end"
	EventHTTPBackendUnreachable = "gateway.http.backend_unreachable"
	EventRateLimited            = "gateway.rate_limit.exceeded"
)

// LogKeyRequestID is the structured-log field for HTTP request correlation.
const LogKeyRequestID = "devplane.request_id"

var (
	jsonAPIErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "devplane",
			Subsystem: "gateway",
			Name:      "json_api_errors_total",
			Help:      "JSON API responses with an error code (auth, workspace_unavailable, workspace_not_ready, …).",
		},
		[]string{"http_status", "error_code"},
	)
	rateLimitHits = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "devplane",
			Subsystem: "gateway",
			Name:      "rate_limit_hits_total",
			Help:      "Requests rejected by gateway rate limiting (lifecycle API or WebSocket connect).",
		},
		[]string{"endpoint", "scope"},
	)
)

// RecordJSONAPIError increments Prometheus counters for a JSON error response.
func RecordJSONAPIError(httpStatus int, code string) {
	jsonAPIErrors.WithLabelValues(strconv.Itoa(httpStatus), code).Inc()
}

// RecordRateLimitHit increments rate-limit rejection counters.
func RecordRateLimitHit(endpoint, scope string) {
	rateLimitHits.WithLabelValues(endpoint, scope).Inc()
}

// RateLimitHitsTotal returns the current value of devplane_gateway_rate_limit_hits_total
// for the given endpoint and scope labels (for tests and ad-hoc inspection).
func RateLimitHitsTotal(endpoint, scope string) float64 {
	return testutil.ToFloat64(rateLimitHits.WithLabelValues(endpoint, scope))
}

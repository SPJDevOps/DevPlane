package gateway

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// Log keys for gateway structured logs (match operator conventions where possible).
	LogKeyComponent = "devplane.component"
	LogKeyEvent     = "devplane.event"
)

// Gateway component value for structured logs.
const ComponentGateway = "gateway"

// Event names for gateway logs.
const (
	EventAuthFailure             = "gateway.auth.failure"
	EventWorkspaceError          = "gateway.workspace.error"
	EventOIDCTokenExchange       = "gateway.oidc.token_exchange.failure"
	EventOIDCInvalidIDToken      = "gateway.oidc.id_token.invalid"
	EventWSProxyStart            = "gateway.ws.proxy.start"
	EventWSProxyBackendNotReady  = "gateway.ws.backend_not_ready"
	EventWSProxySessionEnd       = "gateway.ws.session.end"
	EventHTTPBackendUnreachable  = "gateway.http.backend_unreachable"
)

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
)

// RecordJSONAPIError increments Prometheus counters for a JSON error response.
func RecordJSONAPIError(httpStatus int, code string) {
	jsonAPIErrors.WithLabelValues(strconv.Itoa(httpStatus), code).Inc()
}

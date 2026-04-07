package gateway

import (
	"github.com/go-logr/logr"

	workspacev1alpha1 "workspace-operator/api/v1alpha1"
)

// Stable field keys for JSON log lines (grep / SIEM friendly). All audit records
// include devplane.audit.schema_version and devplane.request_id when a request id exists.
const (
	AuditSchemaVersion = "1"

	LogKeyAuditSchema = "devplane.audit.schema_version"

	// LogKeyActorSubject is the OIDC subject (sub claim).
	LogKeyActorSubject = "actor.subject"
	LogKeyAuditAction  = "action"
	LogKeyAuditOutcome = "outcome"
	LogKeyAuditReason  = "reason"
)

// Outcome values for LogKeyAuditOutcome.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeDenied  = "denied"
)

// Audit event names (devplane.event) — keep stable for dashboards and compliance.
const (
	EventAuditOIDCLoginRedirect       = "devplane.audit.oidc.login.redirect"
	EventAuditOIDCCallbackSuccess     = "devplane.audit.oidc.callback.success"
	EventAuditOIDCCallbackFailure     = "devplane.audit.oidc.callback.failure"
	EventAuditWorkspaceEnsureExists   = "devplane.audit.workspace.ensure_exists"
	EventAuditWorkspaceEnsureRunning  = "devplane.audit.workspace.ensure_running"
	EventAuditWSSessionStart          = "devplane.audit.ws.session.start"
	EventAuditWSSessionEnd            = "devplane.audit.ws.session.end"
	EventAuditAuthTokenRejected       = "devplane.audit.auth.token.rejected"
	EventAuditRateLimitExceeded       = "devplane.audit.rate_limit.exceeded"
)

// EnsureAction returns a stable verb for workspace lifecycle audit: create, restart, or get.
func EnsureAction(d EnsureDetails) string {
	switch {
	case d.Created:
		return "create"
	case d.RestartedFromStopped:
		return "restart"
	default:
		return "get"
	}
}

// LogAudit emits one structured log line for compliance (JSON in production).
// msg is a short human-readable anchor; fields carry the machine-readable payload.
func LogAudit(log logr.Logger, msg, requestID, event string, keysAndValues ...interface{}) {
	args := []interface{}{
		LogKeyAuditSchema, AuditSchemaVersion,
		LogKeyComponent, ComponentGateway,
		LogKeyEvent, event,
	}
	if requestID != "" {
		args = append(args, LogKeyRequestID, requestID)
	}
	args = append(args, keysAndValues...)
	log.Info(msg, args...)
}

// LogWorkspaceLifecycleAudit records ensure_exists / ensure_running after success.
func LogWorkspaceLifecycleAudit(log logr.Logger, msg, requestID, event string, namespace string, claims *Claims, ws *workspacev1alpha1.Workspace, d EnsureDetails) {
	if claims == nil || ws == nil {
		return
	}
	LogAudit(log, msg, requestID, event,
		LogKeyActorSubject, claims.Sub,
		LogKeyUserID, claims.UserID,
		LogKeyNamespace, namespace,
		LogKeyWorkspace, ws.Name,
		LogKeyAuditAction, EnsureAction(d),
		LogKeyAuditOutcome, OutcomeSuccess,
	)
}

// LogOIDCCallbackSuccess records a successful OAuth callback and session cookie issue.
func LogOIDCCallbackSuccess(log logr.Logger, requestID string, claims *Claims) {
	if claims == nil {
		return
	}
	LogAudit(log, "audit: OIDC callback succeeded", requestID, EventAuditOIDCCallbackSuccess,
		LogKeyActorSubject, claims.Sub,
		LogKeyUserID, claims.UserID,
		LogKeyAuditOutcome, OutcomeSuccess,
	)
}

// LogOIDCCallbackFailure records OAuth callback failure without leaking secrets.
func LogOIDCCallbackFailure(log logr.Logger, requestID, reason string) {
	LogAudit(log, "audit: OIDC callback failed", requestID, EventAuditOIDCCallbackFailure,
		LogKeyAuditOutcome, OutcomeFailure,
		LogKeyAuditReason, reason,
	)
}

// LogAuthTokenRejected records API/WS paths where the bearer/cookie token was rejected.
func LogAuthTokenRejected(log logr.Logger, requestID, remote, reason string, httpStatus int, code string) {
	LogAudit(log, "audit: token rejected", requestID, EventAuditAuthTokenRejected,
		LogKeyAuditOutcome, OutcomeDenied,
		"remote", remote,
		LogKeyAuditReason, reason,
		"httpStatus", httpStatus,
		"errorCode", code,
	)
}

// LogRateLimitAudit records a rate-limited request (user scope is already in structured logs).
func LogRateLimitAudit(log logr.Logger, requestID, endpoint, scope, userID string) {
	LogAudit(log, "audit: rate limit exceeded", requestID, EventAuditRateLimitExceeded,
		LogKeyAuditOutcome, OutcomeDenied,
		"endpoint", endpoint,
		"scope", scope,
		LogKeyUserID, userID,
	)
}

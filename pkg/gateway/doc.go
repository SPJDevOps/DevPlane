// Package gateway provides HTTP handlers for the workspace gateway: OIDC
// validation, workspace lifecycle (create/get Workspace CR), and WebSocket
// proxy to user workspace pods.
//
// Workspace lifecycle HTTP API (requires a valid OIDC bearer token, devplane_token
// cookie, or ?token= for WebSocket-only callers):
//
//   - GET /api/workspace — ensure the Workspace CR exists for the authenticated
//     user (create if missing) and return JSON: name, namespace, userId, email,
//     phase, serviceEndpoint, podName, message, ttydReady.
//   - POST /api/workspace — same as GET (for CLI/tools that prefer POST).
//
// Errors use application/json: {"error":"..."} with stable codes for auth
// failures (unauthorized, forbidden). Cluster or provisioning failures return
// 500 with {"error":"workspace_unavailable"} (no internal details).
//
// OIDC configuration (production): set OIDC_ISSUER_URL and OIDC_CLIENT_ID (plus
// OIDC_CLIENT_SECRET and OIDC_REDIRECT_URL for the browser login flow). Optional
// OIDC_AUDIENCE overrides the expected JWT "aud" claim when it differs from the
// OAuth client ID (otherwise the client ID is used).
//
// Development shortcuts: for local API testing without a reachable IdP, set
// GATEWAY_DEV_INSECURE_FIXED_IDENTITY=1 and optionally GATEWAY_DEV_USER_SUB /
// GATEWAY_DEV_USER_EMAIL. Any non-empty bearer token, cookie value, or ?token=
// value is accepted and mapped to those claims; cryptographic verification is
// skipped. Never enable this outside isolated development environments.
package gateway

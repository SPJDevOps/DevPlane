# Gateway: OIDC validation and WebSocket proxy behaviour

This document describes **operator-facing** behaviour for the workspace gateway: how OIDC tokens are validated, which JSON error codes clients see, and how the WebSocket terminal proxy is bounded. It complements the product overview in [README.md](../README.md).

## OIDC ID token validation

- The gateway uses [go-oidc](https://github.com/coreos/go-oidc) with issuer discovery and JWKS signature verification.
- **Audience** defaults to `OIDC_CLIENT_ID`; override with `OIDC_AUDIENCE` when the IdP issues a different `aud` (or for resource-server style clients).
- **Clock skew** — JWT `exp` is compared to gateway time. Set **`OIDC_CLOCK_SKEW`** (Go duration, e.g. `60s`, `2m`) to treat the verifier clock as slightly in the past, so brief NTP skew between the IdP and the gateway does not reject otherwise valid sessions. If unset, the gateway defaults to **60s**. Set to **`0`** to disable skew (strictest expiry check). Helm: `gateway.oidc.clockSkew`.
- **Not-before (`nbf`)** — the underlying library applies a fixed leeway for `nbf` (see go-oidc `verify.go`); do not rely on `OIDC_CLOCK_SKEW` alone for `nbf` edge cases.
- **Caching** — successful verifications are cached in memory (LRU, TTL) keyed by a SHA-256 of the raw token. Revoked tokens may remain usable until cache expiry or process restart; shorten TTL only by changing code or redeploying if your threat model requires faster revocation than the IdP’s token lifetime.

### Token refresh (browser session)

- After the OAuth2 authorization-code flow, the gateway stores the **ID token** in the `devplane_token` HTTP-only cookie (and validates it on each request).
- **Refresh tokens are not stored** by the gateway today. When the ID token expires, the user must complete `/login` again. API clients using `Authorization: Bearer` must obtain a new ID token from their own OAuth2 or device flow.

### Structured auth errors (JSON)

Endpoints that return JSON (`/api/workspace`, `/ws` before WebSocket upgrade) use a single field:

```json
{"error":"<code>"}
```

| HTTP | `error` code       | Meaning |
|------|--------------------|--------|
| 401  | `unauthorized`     | Missing token, invalid signature, malformed JWT, or other verification failure (except below). |
| 401  | `token_expired`    | ID token `exp` is in the past (after clock skew). |
| 403  | `forbidden`        | Plausible token but **audience** (or similar policy) does not match the gateway client. |

Plain browser routes (`/`, `/callback`) redirect to `/login` or return minimal HTML errors instead of JSON.

**Logs** use structured fields (`devplane.component`, `devplane.event`, `devplane.request_id` where applicable). Verification errors never log the raw bearer token or cookie value.

## WebSocket proxy (`/ws`)

- **Upgrade** uses a bounded handshake timeout (see `pkg/gateway` `proxy.go`).
- **Backend dial** uses a dedicated `websocket.Dialer` with the same handshake timeout as the backend dial context, honours **`HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`** for outbound connections from the gateway pod, and fails fast if the workspace ttyd port is unreachable.
- **Frame size** — each direction applies a **1 MiB** read limit per message so a misbehaving client or backend cannot allocate unbounded memory in the gateway.
- **Backpressure** — relay goroutines block on `ReadMessage` / `WriteMessage`; a slow peer naturally slows the other direction (no unbounded in-memory buffering beyond kernel/socket buffers).
- **Session end** — when either side closes or errors, the tunnel ends and the gateway logs `gateway.ws.session.end` with a non-secret reason string.

## Related metrics

- `devplane_gateway_json_api_errors_total{http_status,error_code}` — includes `unauthorized`, `token_expired`, `forbidden`, `workspace_unavailable`, `workspace_not_ready`, `rate_limited`, etc.

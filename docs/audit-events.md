# Structured audit events (gateway + operator)

DevPlane emits **JSON log lines** (Zap production encoder in the gateway; controller-runtime JSON in the operator) with stable field names for compliance and SIEM export. Correlation uses `devplane.request_id`, aligned with the HTTP `X-Request-ID` header on gateway requests.

## Schema

- `devplane.audit.schema_version`: currently `1` (bump only on breaking field changes).
- `devplane.component`: `gateway` or `workspace-controller`.
- `devplane.event`: stable event identifier (see tables below).
- `devplane.request_id`: gateway request correlation id (RFC 4122 UUID when generated).
- `actor.subject`: OIDC `sub` where applicable (gateway).
- `userId`: Kubernetes-safe user id derived from `sub`.
- `namespace`: Workspace CR namespace (tenant boundary).
- `workspace`: Workspace CR name (equals `userId` today).
- `action`: `create` | `get` | `restart` for workspace ensure operations.
- `outcome`: `success` | `failure` | `denied` where applicable.
- `reason`: short machine-readable string for failures (no secrets, no token material).

**Never logged:** bearer tokens, refresh tokens, client secrets, `Authorization` headers, or full cookies.

## Gateway (`devplane.component` = `gateway`)

| devplane.event | When |
|----------------|------|
| `devplane.audit.oidc.login.redirect` | Browser sent to IdP (`/login`). |
| `devplane.audit.oidc.callback.success` | OAuth code exchanged; session cookie set (`/callback`). |
| `devplane.audit.oidc.callback.failure` | Callback failed (state, exchange, missing/invalid id_token). |
| `devplane.audit.workspace.ensure_exists` | After successful `GET|POST /api/workspace` lifecycle resolution. |
| `devplane.audit.workspace.ensure_running` | After workspace is Running and WS path continues (before ttyd upgrade). |
| `devplane.audit.ws.session.start` | WebSocket proxy to ttyd begins. |
| `devplane.audit.ws.session.end` | WebSocket proxy returned (normal or error). |
| `devplane.audit.auth.token.rejected` | Missing/invalid token on API or `/ws`. |
| `devplane.audit.rate_limit.exceeded` | Per-user rate limit hit. |

Operational/diagnostic events (e.g. `gateway.ws.proxy.start`) may still appear alongside audit lines; rely on `devplane.audit.*` events for compliance narratives.

## Operator (`devplane.component` = `workspace-controller`)

| devplane.event | When |
|----------------|------|
| `devplane.audit.workspace.phase_transition` | Workspace `status.phase` changed after a successful status patch. |

## Sample lines (illustrative)

```json
{"level":"info","ts":1712476800.123,"msg":"audit: workspace ensure (API)","devplane.audit.schema_version":"1","devplane.component":"gateway","devplane.event":"devplane.audit.workspace.ensure_exists","devplane.request_id":"8f1c2b3d-...","actor.subject":"oidc|abc","userId":"oidc-abc","namespace":"devplane-system","workspace":"oidc-abc","action":"get","outcome":"success"}
```

```json
{"level":"info","ts":1712476801.456,"msg":"workspace phase transition","devplane.audit.schema_version":"1","devplane.component":"workspace-controller","devplane.event":"devplane.audit.workspace.phase_transition","workspace":"alice","namespace":"devplane-system","userId":"alice","fromPhase":"Creating","toPhase":"Running", ...}
```

## Retention

- Treat audit-bearing streams like security-relevant application logs: **default 90–365 days** depending on policy; shorter for high-volume debug namespaces.
- Keep gateway and operator logs in **separate** indexes or buckets when possible so workspace lifecycle volume can be tiered independently of OIDC/UI traffic.

## Shipping to a SIEM (Vector)

Example: read container stdout JSON, tag by `devplane.event`, forward to Splunk HTTP Event Collector (replace URL/token).

```toml
[sources.k8s_gateway]
type = "kubernetes_logs"
auto_partial_merge = true
extra_label_selector = "app=workspace-gateway"

[transforms.audit_only]
type = "filter"
inputs = ["k8s_gateway"]
condition = '''
  exists(.devplane.audit.schema_version) ||
  string!(.devplane.event) ?? "" =~ /^devplane\\.audit\\./
'''

[sinks.siem_hec]
type = "splunk_hec_logs"
inputs = ["audit_only"]
endpoint = "https://splunk.example:8088/services/collector"
encoding.codec = "json"
```

## Shipping with Fluent Bit

Use a **JSON** parser and filter on key `devplane.event` or prefix `devplane.audit.`. Forward to your SIEM’s HTTP or syslog receiver; map `devplane.request_id` to the vendor’s correlation/session field.

## CEF (optional mapping)

Map roughly as: `rt` ← log timestamp, `suser` ← `userId`, `duser` ← `actor.subject` when present, `msg` ← `devplane.event`, `cs3Label`/`cs3` ← `workspace` / `namespace`, `externalId` ← `devplane.request_id`. Adjust per ArcSight/Elastic CEF extensions your SIEM expects.

## OpenTelemetry (optional)

For log bridges that emit OTLP logs, attach `devplane.event` as `log.record.attributes["devplane.event"]` and `devplane.request_id` as a correlation attribute. Trace linkage is not automatic today; request id is the stable join key across gateway log lines.

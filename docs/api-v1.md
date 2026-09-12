# AEP v1 API Guide

[简体中文](api-v1.zh-CN.md) | English

Protocol profile: `1.0.0-rc.1`

This guide documents the AEP v1 HTTP(S) REST API. The machine-readable
contracts are the [core OpenAPI document](../openapi/aep-v1.openapi.yaml), the
[Control Events OpenAPI document](../openapi/aep-v1-control-events.openapi.yaml),
and the [Authentication OpenAPI document](../openapi/aep-v1-authentication.openapi.yaml).

## 1. Conventions

HTTPS deployment example: `https://enterprise.example.com/aep/v1`
HTTP deployment example: `http://enterprise.example.com/aep/v1`
Local example: `http://localhost:8080/aep/v1`

Authenticated request headers:

```http
Authorization: Bearer <access-token>
X-AEP-Protocol-Version: 1.0
X-Request-ID: <request-id>
```

JSON properties use `camelCase`. Timestamps use RFC 3339 UTC. Errors use RFC
9457 `application/problem+json`. AEP v1 uses REST polling rather than SSE or
WebSocket.

Service metadata may be queried without a protocol-version header so a client can discover compatibility. Every other /aep/v1 request must send X-AEP-Protocol-Version: 1.0. A missing or unsupported value returns RFC 9457 status 426 with X-AEP-Supported-Protocol-Versions.

## 2. Endpoints

### Service and authentication

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/.well-known/jwks.json` | Public keys for AEP and model-token verification |
| GET | `/metadata` | Supported AEP versions and features |
| GET | `/auth/methods` | Discover login methods for a deployment |
| POST | `/auth/password/login` | Sign in with an administrator-provisioned ZhiYuan account |
| POST | `/auth/password/change` | Replace the current account password |
| POST | `/auth/federated/start` | Start customer federated login |
| POST | `/auth/exchange` | Exchange a federated one-time authorization code |
| POST | `/auth/refresh` | Refresh a session |
| POST | `/auth/logout` | Revoke the current refresh session |
| POST | `/user/activation` | Exchange the authenticated deployment session for an entitlement token |

Federated endpoints are reserved for a configured identity adapter. The built-in
mock is development-only and is not advertised by the password-only production
profile.

### User runtime API

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/user/me` | Current user, deployment, session, roles, and permissions |
| GET | `/user/skills/manifest` | Complete desired Skill manifest |
| GET | `/user/skills/{skillId}/versions/{version}/package` | Download Skill ZIP |
| POST | `/user/skills/sync-results` | Report Skill synchronization |
| POST | `/user/events/batch` | Upload an idempotent event batch |
| POST | `/user/heartbeat` | Report session liveness and discover pending control events |
| GET | `/user/control-events` | Retrieve applicable unacknowledged control events |
| POST | `/user/control-events/{deliveryId}/acknowledge` | Confirm durable receipt |
| POST | `/user/control-events/{deliveryId}/result` | Report task execution state |
| GET | `/user/credentials` | List assigned credential metadata |
| POST | `/user/credentials/{credentialId}/resolve` | Retrieve a client-deliverable secret |
| GET | `/user/models` | List visible models |

## 3. Metadata and Authentication

### `GET /metadata`

```json
{
  "service": "aep-control-service",
  "supportedProtocolVersions": ["1.0"],
  "capabilities": ["password_auth", "skills", "telemetry", "control_events", "model_gateway", "credentials"],
  "jwksUri": "/.well-known/jwks.json",
  "deploymentId": "deployment_001",
  "deployment": {"id": "deployment_001", "name": "Example Deployment"},
  "modelGateway": {"baseUrl": "https://gateway.example.com/v1", "protocol": "openai-compatible", "apiVersion": "v1"}
}
```

### `GET /auth/methods`

Example: `GET /auth/methods?deploymentHint=deployment_001`

```json
{
  "deployment": {"id": "deployment_001", "name": "Example Deployment"},
  "deploymentId": "deployment_001",
  "preferredMethodId": "zhiyuan-password",
  "methods": [
    {"id": "zhiyuan-password", "type": "password", "displayName": "ZhiYuan account"}
  ]
}
```

### `POST /auth/password/login`

```json
{
  "deploymentId": "deployment_001",
  "sessionId": "terminal_01",
  "username": "liming",
  "password": "user-entered-password"
}
```

The account is created or batch-imported by an administrator. Public
registration is not implied. Password login may use HTTP or HTTPS in every
deployment stage. HTTPS is strongly recommended outside a trusted private
network because plain HTTP exposes credentials and bearer tokens in transit.

### `POST /auth/password/change`

Request: `{"currentPassword":"old-password","newPassword":"new-long-password"}`.
The authenticated account password is replaced, its other refresh sessions are
revoked, and the response returns a fresh token structure with
`passwordChangeRequired` set to false.

### `POST /auth/federated/start`

```json
{
  "deploymentId": "deployment_001",
  "sessionId": "terminal_01",
  "methodId": "customer-sso",
  "redirectUri": "zhiyuan://auth/callback",
  "codeChallenge": "base64url-sha256-challenge"
}
```

```json
{
  "transactionId": "login_tx_123",
  "authorizationUrl": "https://idp.example.com/authorize?...",
  "state": "opaque-state",
  "expiresIn": 300
}
```

The Agent opens `authorizationUrl` in the system browser and verifies `state`
on callback. Customer credentials never pass through the Agent.

### `POST /auth/exchange`

```json
{
  "transactionId": "login_tx_123",
  "authorizationCode": "one-time-code",
  "redirectUri": "zhiyuan://auth/callback",
  "codeVerifier": "pkce-verifier"
}
```

```json
{
  "accessToken": "eyJ...",
  "refreshToken": "refresh-token",
  "modelAccessToken": "eyJ-model...",
  "tokenType": "Bearer",
  "expiresIn": 7200,
  "modelAccessExpiresIn": 7200,
  "deploymentId": "deployment_001",
  "sessionId": "terminal_01",
  "passwordChangeRequired": false
}
```

Password login and federated exchange return this same session structure. The
authorization code is single use. The model token is accepted directly by the
Model Gateway during its validity period.

When `passwordChangeRequired` is true, the session can only read current
identity, change the password, or log out. Its model token has no model scopes.
Other operations return `PASSWORD_CHANGE_REQUIRED`. Password login failures
use a shared progressive backoff and return `429` with `Retry-After` while the
backoff is active.

### `POST /auth/refresh`

Request:

```json
{"refreshToken": "refresh-token", "sessionId": "terminal_01"}
```

The response uses the token schema above. A returned refresh token replaces
the previous token.

### `POST /auth/logout`

Request: `{"refreshToken":"refresh-token"}`. Success returns `204 No Content`.

### `POST /user/activation`

The authenticated client submits an empty request. The Control Service uses
the License verified and registered for its deployment before issuing a
short-lived service-signed entitlement token:

```json
{}
```

The response includes `entitlementToken`, `expiresAt`, and the normalized
feature list. The token is bound to the authenticated deployment, user, and
session. Production model gateways recheck the current session and requested
model assignment within their configured status-cache window.
The Control Service does not sign licenses and must never receive a license
private key.

## 4. Current Identity

### `GET /user/me`

```json
{
  "user": {"id": "user_123", "displayName": "Li Ming", "email": "liming@example.com"},
  "deployment": {"id": "deployment_001", "name": "Example Deployment"},
  "deploymentId": "deployment_001",
  "sessionId": "terminal_01",
  "roles": ["employee"],
  "permissions": ["models.read", "skills.read"],
  "sessionExpiresAt": "2026-08-19T10:00:00Z",
  "passwordChangeRequired": false
}
```

## 5. Skill Synchronization

### `GET /user/skills/manifest`

The client SHOULD send the previous `ETag` in `If-None-Match`. The server
returns `304 Not Modified` when unchanged.

```json
{
  "revision": "43",
  "generatedAt": "2026-08-19T08:00:00Z",
  "skills": [{
    "id": "docx",
    "name": "Word Documents",
    "description": "Create and edit Word documents.",
    "version": "1.3.0",
    "enabled": true,
    "package": {
      "url": "/aep/v1/user/skills/docx/versions/1.3.0/package",
      "sha256": "8be72b3f1f47e36014fc8e1af54b250098201b1e2ea9a260153d69f7e64f1930",
      "size": 18342
    }
  }]
}
```

The response is complete. Managed Skills absent from it are removed.

### `GET /user/skills/{skillId}/versions/{version}/package`

Returns `application/zip`. The server rechecks assignment, and the client
verifies the manifest SHA-256 before extraction.

### `POST /user/skills/sync-results`

```json
{
  "manifestRevision": "43",
  "startedAt": "2026-08-19T08:01:00Z",
  "completedAt": "2026-08-19T08:01:03Z",
  "status": "partial",
  "items": [
    {"skillId": "docx", "version": "1.3.0", "action": "update", "status": "success"},
    {
      "skillId": "xlsx",
      "version": "2.0.0",
      "action": "install",
      "status": "failed",
      "errorCode": "PACKAGE_HASH_MISMATCH",
      "message": "Downloaded package digest did not match the manifest."
    }
  ]
}
```

## 6. Telemetry Event Upload

### `POST /user/events/batch`

```json
{
  "events": [{
    "eventId": "0198a91b-d0d4-70a1-b38e-c43482f6798d",
    "type": "skill.updated",
    "occurredAt": "2026-08-19T08:01:03Z",
    "resource": {"type": "skill", "id": "docx"},
    "result": "success",
    "metadata": {"fromVersion": "1.2.0", "toVersion": "1.3.0"}
  }]
}
```

```json
{"accepted": ["0198a91b-d0d4-70a1-b38e-c43482f6798d"], "rejected": []}
```

Rejected items contain `eventId`, `code`, and `message`. Duplicate IDs are
accepted, allowing safe retry.

## 7. Control Events

### `POST /user/heartbeat`

The heartbeat reports liveness and returns only control-event discovery metadata.

```json
{
  "lastControlEventCursor": "142",
  "status": "online"
}
```

```json
{
  "serverTime": "2026-08-19T08:00:00Z",
  "controlEvents": {
    "pending": true,
    "watermark": "147"
  },
  "nextHeartbeatAfterSeconds": 30
}
```

The pending flag is an optimization, not a correctness boundary. The Agent
still performs periodic control-event queries so that a stale flag cannot
permanently hide an event.

### `GET /user/control-events`

Query parameters are `afterCursor` and `limit`. The server derives applicable
`global`, `team`, `role`, and `user` scopes from the authenticated user and its
role/team bindings.

```json
{
  "items": [{
    "deliveryId": "delivery_001",
    "eventId": "event_001",
    "cursor": "143",
    "type": "skill.manifest.changed",
    "scope": {"type": "team", "id": "team_001"},
    "resource": {"type": "skill", "id": "docx", "revision": "18"},
    "task": {"type": "skill.reconcile"},
    "createdAt": "2026-08-19T07:59:00Z",
    "expiresAt": "2026-08-20T07:59:00Z"
  }],
  "nextCursor": "143",
  "watermark": "147"
}
```

Reading this response does not consume an event. Until receipt is
acknowledged, the server may return the delivery again. The Agent deduplicates
by `deliveryId` and `eventId`.

### `POST /user/control-events/{deliveryId}/acknowledge`

The Agent calls this endpoint only after committing the event to its durable
local inbox. The operation is idempotent.

```json
{
  "status": "received",
  "receivedAt": "2026-08-19T08:00:01Z"
}
```

A successful acknowledgement returns `204 No Content`. Receipt acknowledgement
ends network redelivery but does not mean the Task succeeded.

### `POST /user/control-events/{deliveryId}/result`

The Agent reports `running`, `succeeded`, or `failed`. Repeating the same state
and result is idempotent.

```json
{
  "status": "succeeded",
  "completedAt": "2026-08-19T08:00:03Z",
  "appliedRevision": "18"
}
```

Failure example:

```json
{
  "status": "failed",
  "completedAt": "2026-08-19T08:00:03Z",
  "errorCode": "SKILL_RECONCILE_FAILED",
  "message": "The Skill package could not be installed.",
  "retryable": true
}
```

The server keeps receipt and execution states separately. Each applicable user
session has its own delivery record even when the source event has global,
team, role, or user scope. Active unexpired events are attached to a matching
session when that session is established, so the user need not be online when
the event is published.

## 8. Credentials

### `GET /user/credentials`

```json
{
  "credentials": [{
    "id": "credential_123",
    "name": "Internal Search API",
    "service": "internal-search",
    "type": "api_key",
    "deliveryMode": "client",
    "maskedValue": "sk-****9f2a",
    "enabled": true,
    "updatedAt": "2026-08-19T07:00:00Z"
  }],
  "nextCursor": null
}
```

### `POST /user/credentials/{credentialId}/resolve`

Request: `{"purpose":"Connect to the internal search service"}`.

```json
{
  "credentialId": "credential_123",
  "type": "api_key",
  "value": "sk-live-value",
  "expiresAt": null
}
```

The response includes `Cache-Control: no-store`. `server_only` credentials
return `CREDENTIAL_NOT_DELIVERABLE`.

## 9. Models

### `GET /user/models`

```json
{
  "models": [
    {
      "id": "enterprise-qwen-32b",
      "displayName": "Enterprise Qwen 32B",
      "sourceType": "enterprise_open_source",
      "protocol": "openai-compatible",
      "endpoint": "https://models.example.com/v1",
      "upstreamModel": "qwen3-32b",
      "capabilities": ["text", "tools", "streaming"],
      "contextWindow": 131072,
      "isDefault": true,
      "enabled": true
    }
  ]
}
```

For remote models, the Agent authenticates to the declared gateway endpoint.
It sends the model access token obtained at login or refresh. The gateway
validates the token locally per request and does not synchronously call the
Control Service for a new authorization decision. AEP does not redefine
inference payloads.

## 10. Administration API

Administrative endpoints require an administrator identity.

### Platform accounts

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/admin/users` | List or manually create ZhiYuan platform accounts |
| POST | `/admin/users/import` | Batch-import ZhiYuan platform accounts |
| PATCH | `/admin/users/{userId}` | Enable, disable, or update an account |
| POST | `/admin/users/{userId}/reset-password` | Set a new temporary password |

Every user must have at least one role and one team when created or imported.

### RBAC and sessions

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/admin/permissions` | Read the stable permission catalog |
| GET, POST | `/admin/roles` | List or create deployment roles |
| GET, PATCH, DELETE | `/admin/roles/{roleId}` | Read, update, or delete a non-system role |
| GET, POST | `/admin/teams` | List or create deployment teams |
| GET, PATCH, DELETE | `/admin/teams/{teamId}` | Read, update, or delete a team |
| PUT | `/admin/users/{userId}/rbac` | Replace a user's role and team bindings |
| GET | `/admin/sessions` | List user sessions and heartbeat state |
| POST | `/admin/sessions/{sessionId}/revoke` | Revoke one user session |

### Skills

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/admin/skills` | List or create Skill metadata |
| GET, PATCH, DELETE | `/admin/skills/{skillId}` | Read, update, or withdraw a Skill |
| POST | `/admin/skills/{skillId}/versions` | Upload ZIP by multipart form data |
| POST | `/admin/skills/{skillId}/versions/{version}/publish` | Publish a version |
| DELETE | `/admin/skills/{skillId}/versions/{version}` | Withdraw a version |
| GET, POST | `/admin/skill-assignments` | List or create assignments |
| DELETE | `/admin/skill-assignments/{assignmentId}` | Remove an assignment |

Skill IDs are portable identifiers of at most 64 characters. Version
identifiers are at most 128 characters. Both must start and end with a letter
or digit; Skill IDs may contain `.`, `_`, and `-`, while versions additionally
allow `+`. Path separators and traversal segments are not accepted.

Assignment example:

```json
{"skillId": "docx", "subject": {"type": "role", "id": "employee"}}
```

Skill, credential, and model assignments accept `user`, `role`, or `team`
subjects. Effective access is the union of the current user's direct, role,
and team assignments.

### Credentials

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/admin/credentials` | List masked metadata or create a credential |
| GET, PATCH, DELETE | `/admin/credentials/{credentialId}` | Read, update, or revoke |
| POST | `/admin/credentials/{credentialId}/rotate` | Replace secret material |
| GET, POST | `/admin/credential-assignments` | List or create assignments |
| DELETE | `/admin/credential-assignments/{assignmentId}` | Remove an assignment |

Create example:

```json
{
  "name": "Internal Search API",
  "service": "internal-search",
  "type": "api_key",
  "deliveryMode": "client",
  "value": "sk-live-value",
  "enabled": true
}
```

Read responses never return `value`. Rotation accepts a new `value`.

### Models

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/admin/models` | List or create model descriptors |
| GET, PATCH, DELETE | `/admin/models/{modelId}` | Read, update, or remove a model |
| GET, POST | `/admin/model-assignments` | List or create assignments |
| DELETE | `/admin/model-assignments/{assignmentId}` | Remove an assignment |

Administrative `credentialId` MUST NOT appear in the Agent model catalog.

### Licenses

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/admin/licenses` | List registered deployment License metadata |
| GET | `/admin/licenses/{licenseId}` | Read License metadata and activation state |
| POST | `/admin/licenses/import` | Register a vendor-signed offline License |
| POST | `/admin/licenses/{licenseId}/revoke` | Revoke a registered License |

The Control Service verifies the vendor signature with configured public-key
material. License private keys and signer code are outside this repository and
must never be sent to the service.

### Data plane

| Method | Path | Purpose |
| --- | --- | --- |
| GET, PUT | `/admin/data-plane/desired-state` | Read or publish model-gateway desired state |
| GET | `/admin/data-plane/status` | Read the latest reconciliation observation |

Desired state refers to external Secret names and keys only; it never includes
provider secret values.

### Control Events

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/admin/control-events` | Search or publish control events |
| GET | `/admin/control-events/{eventId}` | Read one event and aggregate status |
| POST | `/admin/control-events/{eventId}/cancel` | Cancel pending deliveries |
| GET | `/admin/control-events/{eventId}/deliveries` | Inspect per-session delivery status |

Publish example:

```json
{
  "type": "skill.manifest.changed",
  "scope": {"type": "team", "id": "team_001"},
  "resource": {"type": "skill", "id": "docx", "revision": "18"},
  "task": {"type": "skill.reconcile"},
  "expiresAt": "2026-08-20T07:59:00Z",
  "supersedesKey": "skill:docx:team_001"
}
```

The server resolves matching user sessions. Cancellation affects only deliveries
that have not reached `received`; it does not undo work already accepted by a client.
Publishing a newer event with the same `supersedesKey` marks older pending
deliveries as `superseded`.

### Telemetry Events

`GET /admin/events` supports `cursor`, `limit`, `userId`, `sessionId`, `type`,
`resourceType`, `resourceId`, `result`, `occurredAfter`, and `occurredBefore`.

## 11. Errors and Retry

```json
{
  "type": "https://aep.example/problems/model-not-allowed",
  "title": "Model access denied",
  "status": 403,
  "detail": "The current user is not allowed to use this model.",
  "code": "MODEL_NOT_ALLOWED",
  "requestId": "0198..."
}
```

Common codes are `INVALID_REQUEST`, `TOKEN_INVALID`, `ACCESS_DENIED`,
`SKILL_NOT_ASSIGNED`, `CREDENTIAL_NOT_DELIVERABLE`, `MODEL_NOT_ALLOWED`,
`RESOURCE_NOT_FOUND`, `VERSION_CONFLICT`, `RATE_LIMITED`, and `INTERNAL_ERROR`.

Retry safe reads with exponential backoff. Event batches are idempotent by
`eventId`. Do not blindly retry authorization-code exchange or credential
resolution. Honor `Retry-After` on `429` and `503`.

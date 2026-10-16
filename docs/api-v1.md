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

Served by the agent control protocol service in split deployments (`openapi/aep-v1-agent.openapi.yaml`); the all-in-one control-service mounts the same routes.

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
  "password": "user-entered-password",
  "client": {"name": "zhiyuan-desktop", "version": "1.4.0", "deviceId": "device_42"}
}
```

The account is created or batch-imported by an administrator. Public
registration is not implied. Leading and trailing whitespace in `password` is
trimmed before verification. Password login may use HTTP or HTTPS in every
deployment stage. HTTPS is strongly recommended outside a trusted private
network because plain HTTP exposes credentials and bearer tokens in transit.

`client` is optional. When present it is recorded on the issued session and
surfaced by `GET /admin/sessions` so administrators can tell terminals apart
before revoking one. `name` is required (1-64 characters); `version` (up to 64
characters) and `deviceId` (up to 128 characters) are optional. When the field
is omitted the server derives a coarse label from User-Agent (`browser`,
`electron`, `node`, or `curl`) and otherwise leaves the identity unknown.
These values are self-reported administrative hints, not verified claims.

### `POST /auth/password/change`

Request: `{"newPassword":"new-long-password"}`. The session bearer authenticates
the change. `currentPassword` is an optional step-up proof: when provided it is
trimmed of leading and trailing whitespace, verified, and a mismatch fails with
`401 INVALID_CREDENTIALS`; when omitted the session alone authorizes the change.
`newPassword` must not start or end with whitespace (`PASSWORD_POLICY_VIOLATION`).
The account password is replaced, its other refresh sessions are revoked, and
the response returns a fresh token structure with `passwordChangeRequired` set
to false.

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

Returns `application/zip`. The server rechecks the same effectiveness rules as
the manifest - assignment, grant expiry, Skill enablement and version
publication - so a Skill the client does not see in its manifest cannot be
downloaded. The client verifies the manifest SHA-256 before extraction.

### `POST /user/skills/sync-results`

```json
{
  "revision": "43",
  "status": "completed",
  "items": [
    {"skillId": "docx", "status": "updated"},
    {"skillId": "xlsx", "status": "installed"}
  ]
}
```

The server reads the report's `revision`, `status` and `items`, and each item's
`skillId` and `status` (`installed`, `updated` and `unchanged` count as
installed; `removed` and any other value count as not installed). The report's
`startedAt` and `completedAt`, and each item's `version`, `action`, `errorCode`
and `message`, are accepted and kept in the stored payload but drive no server
behaviour. Any field not listed above is refused with `400`, and a report that
carries `manifestRevision` instead of `revision` is refused with `400` too.

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

An optional `client` object (same shape as on password login) replaces the
client identity recorded on the session, so a client version upgrade becomes
visible in `GET /admin/sessions` without a new login. Unlike login, no
User-Agent fallback is applied here; sessions keep their stored identity when
the field is absent.

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

`watermark` is the largest `cursor` delivered to the session so far, or `""`
when nothing has been delivered; it matches the heartbeat
`controlEvents.watermark`. It is informational: page with `nextCursor`
(`afterCursor` takes the last returned delivery's `cursor`), because the
watermark can be ahead of deliveries that are still pending.

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

Every user must have at least one role and one team when created or imported. Passwords must contain 6 to 1024 characters. Temporary passwords must not start or end with whitespace: create and reset reject it (`PASSWORD_POLICY_VIOLATION`), while import trims it because imported rows come from exports and spreadsheets. An import that rejects every row is a failure, not an empty success: it answers `422 USER_IMPORT_REJECTED` with the per-row reasons attached to the problem document, because a batch that created no account did not apply. A batch that created at least one account stays `200` and reports `created` and `rejected` counts. When create or import omits the temporary password (or import trims it to empty), the server assigns the default `123456`. Every account carries a `kind` label (`human` or `agent`); the platform user listing returns human accounts only, and digital employees (`kind=agent`) are listed by the agent directory. The current-identity response labels the authenticated principal the same way. Resetting the caller's own password with `requirePasswordChange=true` is rejected with `409 SELF_PASSWORD_RESET_RESTRICTED`; use the self-service password change endpoint or pass `requirePasswordChange=false` instead.

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

`GET /admin/sessions` includes an optional `client` object per item
(`{"name": "zhiyuan-desktop", "version": "1.4.0", "deviceId": "device_42"}`),
which is `null` when no client identity is known. It distinguishes terminals of
the same account — for example an Admin Console browser login from a managed
desktop client — before an administrator revokes one.

Teams form a forest. `POST /admin/teams` accepts an optional `parentId`, and
every team carries a `path` and `depth` that are fixed at creation; parents
never move afterwards, so cyclic hierarchies are impossible. Deleting a team
that is still referenced returns `TEAM_HAS_CHILDREN` (409) when it has child
teams, `TEAM_HAS_AGENTS` (409) when it is the home team of digital employees,
or `TEAM_IN_USE` (409) for any other foreign-key reference. Grants that point at a subject through the polymorphic subject columns (skill, model, and credential assignments, plus data-scope rules) carry no foreign key: deleting the role or team removes them in the same transaction instead of blocking the delete, so a later subject that reuses the same identifier never inherits them.

### Digital employees

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/admin/agents` | List or create digital employee accounts |
| DELETE | `/admin/agents/{agentId}` | Delete a digital employee account |
| PUT | `/admin/agents/{agentId}/profile` | Update the profile of one digital employee |

Reads require the `users.read` permission; writes accept `agents.write` (the
delegable digital-employee lifecycle grant that cannot touch human accounts)
or `users.write`. Full administrators always qualify. The deployment may cap
resident (non-ephemeral) digital employees via `AEP_MAX_RESIDENT_AGENTS`;
exceeding it returns `AGENT_QUOTA_EXCEEDED` (409). A digital employee is a platform account
with a home team, a display title, an optional description, and an optional
prompt Skill. The home team is always granted in addition to `teamIds`, and
the caller must be allowed to grant every requested role and team.

`password` must contain at least 8 characters and is stored only as an
Argon2id hash. `GET /admin/agents` paginates with `cursor` and `limit`
(1-200, default 50) and returns `agents` plus a `nextCursor` that is `null`
on the last page.

Profile updates accept `displayTitle`, `description`, `homeTeamId`, and
`promptSkillId`; each field may be set to null to clear the stored value.

Ephemeral conversation-scoped instances: `ephemeral: true` requires a future
`expiresAt`; once it passes, login and refresh reject the account with
`AGENT_EXPIRED` (403). `scopeFromUserId` freezes the referenced user's
data-scope snapshot onto the account (visible team subtrees become
management_scope rules, explicit denies are copied), and the requested teams
must already lie inside that scope. `modelIds` creates model assignments
together with the account. The directory hides ephemeral instances unless
`includeEphemeral=true` is passed. `DELETE /admin/agents/{agentId}` removes
the account with its bindings, assignments, and rules; live sessions block
the deletion with `AGENT_HAS_SESSIONS` (409) until revoked.

Error codes:

| Code | Status | Meaning |
| --- | --- | --- |
| `INVALID_AGENT` | 400 | Invalid username, display name, password, home team, prompt Skill, or unknown role or team |
| `USER_RBAC_REQUIRED` | 400 | At least one role and one team (including the home team) are required |
| `INVALID_ROLE`, `INVALID_TEAM` | 400 | Invalid role or team binding |
| `ROLE_GRANT_FORBIDDEN`, `TEAM_GRANT_FORBIDDEN` | 403 | The caller cannot grant a requested role or team |
| `AGENT_EXISTS` | 409 | The username already exists |
| `INVALID_SCOPE_SOURCE` | 400 | The scope source user is unknown, or the requested teams lie outside the source's visible scope |
| `UNKNOWN_MODEL` | 400 | A requested model assignment references an unknown model |
| `AGENT_HAS_SESSIONS` | 409 | Deletion is blocked by live sessions; revoke them first |
| `AGENT_EXPIRED` | 403 | The ephemeral account's expiry has passed |

### Identity sources

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/admin/identity-sources` | List or register external identity sources |
| GET, PUT | `/admin/identity-sources/{sourceId}/mappings` | List or upsert subject mappings |
| DELETE | `/admin/identity-sources/{sourceId}/mappings/{subjectType}/{externalId}` | Remove one mapping |

Reads require the `identity.read` permission and writes require
`identity.write`. `kind` is a protocol category only (`ldap`, `oidc`, or
`directory`); product-specific connector flavors are expressed inside
`config` on the deployment side and are not protocol vocabulary. `config`
must be a plain JSON object and must not contain secret keys such as
`password`, `secret`, `token`, `apikey`, `clientsecret`, or `bindpassword`;
secret material may only be expressed as credential store references. Stored
`config` is never echoed by reads.

Both list endpoints paginate with `cursor` and `limit`; mapping lists also
accept an optional `subjectType` filter (`user` or `team`). List responses
return a `nextCursor` that is `null` on the last page.

Error codes:

| Code | Status | Meaning |
| --- | --- | --- |
| `INVALID_IDENTITY_SOURCE` | 400 | Invalid id, kind, or display name; or a `config` that is not a JSON object or contains secret keys |
| `IDENTITY_SOURCE_EXISTS` | 409 | The identity source id already exists |
| `INVALID_IDENTITY_MAPPING` | 400 | Missing or invalid subject type, external id, or local subject id |
| `RESOURCE_NOT_FOUND` | 404 | The identity source was not found |

### Data scope

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/admin/data-scope-rules` | List or create data scope rules |
| GET, DELETE | `/admin/data-scope-rules/{ruleId}` | Read or delete one rule |
| GET | `/admin/data-scope/context` | Resolve the retrieval context of one user |

Reads require the `data_scope.read` permission and writes require
`data_scope.write`. A rule binds a subject (`user`, `role`, or `team`) to a
resource with the rule kind `management_scope`, `exception_grant`, or
`explicit_deny`. `resourceKind` is either the protocol-defined `team` or a
deployment-defined lowercase identifier; the protocol does not enumerate
deployment kinds and treats them as opaque references. `startsAt` and
`expiresAt` mark a temporary window, and an expired rule stops authorizing.

`GET /admin/data-scope/context?userId=...` derives the retrieval context of
one user from stored bindings and rules: `orgScope` is the subtree of the
user's teams, `allowedResources` and `deniedResources` are `(kind, id)`
references where every non-`team` kind is deployment-defined, and an explicit
deny wins over an allow.

Rule lists paginate with `cursor` and `limit` and return a `nextCursor` that
is `null` on the last page.

Error codes:

| Code | Status | Meaning |
| --- | --- | --- |
| `INVALID_DATA_SCOPE_RULE` | 400 | Invalid id, rule kind, subject, or resource |
| `DATA_SCOPE_RULE_EXISTS` | 409 | The rule id already exists |
| `USER_REQUIRED` | 400 | The `userId` query parameter is required |
| `RESOURCE_NOT_FOUND` | 404 | The rule or user was not found |

### Skills

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/admin/skills` | List or create Skill metadata |
| GET, PATCH, DELETE | `/admin/skills/{skillId}` | Read, update, or delete a Skill |
| POST | `/admin/skills/{skillId}/versions` | Upload ZIP by multipart form data |
| POST | `/admin/skills/{skillId}/versions/{version}/publish` | Publish a version |
| DELETE | `/admin/skills/{skillId}/versions/{version}` | Withdraw a version |
| GET, POST | `/admin/skill-assignments` | List or create assignments |
| DELETE | `/admin/skill-assignments/{assignmentId}` | Remove an assignment |
| PATCH | `/admin/skill-assignments/{assignmentId}` | Change when an assignment ends (`expiresAt` RFC3339, or `null` for perpetual) |

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

An `endpoint` MUST NOT embed credentials: a URL carrying userinfo is rejected
with 400 `INVALID_MODEL`, and secrets belong in `credentialId`. See
[Administrative audit](#administrative-audit) for how an endpoint is recorded.

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
| POST | `/admin/data-plane/publish` | Derive gateway routes from the model catalog and atomically replace desired state |
| GET, PUT | `/admin/data-plane/desired-state` | Read desired state; PUT is the deprecated manual escape hatch for operator-authored states |
| GET | `/admin/data-plane/status` | Read the latest reconciliation observation and the catalog drift comparison |

Desired state refers to external Secret names and keys only; it never includes
provider secret values. Catalog-derived routes for Credential-bound models
reference the conventional Secret `aep-credential-<credentialId>` (key
`api-key`) in the dedicated `aep-credentials` namespace, which the deployment
Secret system provisions. The namespace is resolved server-side, so a
`credentialRef.namespace` sent by a client is ignored.

#### Asynchronous operator publication (queue stage)

The Control Service implements durable queueing and operation queries behind
`AEP_GATEWAY_PUBLICATIONS_ENABLED` (default `false`); disabled deployments
return `503 DATA_PLANE_PUBLICATIONS_DISABLED`. A separate
`AEP_GATEWAY_PUBLICATION_RELAY_ENABLED=true` opt-in runs the in-cluster relay
for the configured deployment in `aep-system`. Without it requests stay queued.
The relay projects and verifies AEP CRs. The latest projected operation can
expose matching operator Accepted/Applied evidence; Ready requires fresh
independent full-fleet Envoy configuration evidence and otherwise stays Unknown.
Opt in only during isolated acceptance and reviewed operator handover.
Legacy deployments keep synchronous `POST /publish` (`200 DataPlaneDesiredState`).
The relay implies operator mode; `AEP_GATEWAY_OPERATOR_MODE=true` keeps the
fence and read-only observer active when relay delivery is paused. Both require
publications enabled and namespace `aep-system`. Operator mode rejects legacy
route PUT/publish, limits publish/status and all four internal reconciler
read/write endpoints with `503 LEGACY_GATEWAY_PUBLICATION_DISABLED`, after
authentication/authorization. Draft CRUD and runtime quota APIs stay available.
There is no automatic fallback between paths.

In operator mode existing desired-state/status GETs read the latest selected
immutable publication, including queued/failed intent. Status compares the
catalog against that intent and uses matching operator observations. Applied
means `applying`; only fresh independent Envoy evidence means `ready`. Missing
or stale evidence stays pending. Reads never fall back to legacy tables.
`contentHash` identifies the full publication (routes and selected limits).
Clients must compare the two GET hashes and retry both reads on mismatch;
a publication change during a read returns 503. Responses are `no-store`.
Do not switch production clients until relay recovery, operator exclusive
writing and end-to-end effectiveness have passed acceptance.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/admin/data-plane/publications` | Durably queue a catalog-derived route snapshot; return `202 DataPlanePublication` and `Location` |
| GET | `/admin/data-plane/publications/{operationId}` | Read delivery progress and version-specific operator conditions |
| POST | `/admin/data-plane/publications/{operationId}/replay` | Recovery replay of the latest selected immutable intent, with a new operation ID |

Recovery replay requires `data_plane.write`, a fresh `Idempotency-Key` and body
`{}`. It copies the selected snapshot, including policies, without capturing
drafts. Unknown/cross-deployment sources return 404; a source no longer latest
returns `409 PUBLICATION_NOT_LATEST`; pending reference withdrawals return
`409 PUBLICATION_REFERENCES_CHANGED`. Reusing the same key returns the same
new operation before evaluating the latest-intent check. Conflicting key reuse
returns `409 IDEMPOTENCY_KEY_CONFLICT`. A 202 is durable acceptance only.
The SDK exposes `replayDataPlanePublication(operationId, idempotencyKey)`.
This is not historical rollback or a way around Kubernetes ownership/sequence
fences. Restore PostgreSQL and Kubernetes together when their sequence diverges.

Both require `data_plane.write`, the same permission as existing data-plane
management, and derive deployment scope from the authenticated session. A
missing, expired or other deployment's operation returns `404 RESOURCE_NOT_FOUND`.
Responses use `Cache-Control: no-store`; no raw CRs or resolved credentials
are exposed. `includeGatewayLimits: true` captures draft rules in the same
immutable snapshot under the existing `data_plane.write` permission.
Omission/false preserves the latest publication's captured policies, even if
still queued; it never reads draft rules or resets published limits. Global
and user/team/role rules without a model remain shared across models. Disabled
and removed rules retain fenced tombstones. This does not change model access
assignments: access remains separate from quota scope. This additive contract
requires the policy-capable relay/operator implementation before use.

Send a required `Idempotency-Key` header (1–128 ASCII letters, digits or
`._:-`) and a JSON body `{}` or `{"revision":"release-1"}`. Never use a secret
as the key. Surrounding revision whitespace is trimmed; an explicitly empty
revision or a revision already bound to different route content is rejected
with `422 INVALID_DATA_PLANE_STATE`. Labels are not sequence numbers.
Malformed JSON or an invalid/missing key returns `400`; invalid keys use
`INVALID_IDEMPOTENCY_KEY`. Failed validation commits no operation/outbox intent.

A first `202` means the consistent catalog snapshot, operation, audit event
and outbox intent committed in one DB transaction. It does not mean a CR or
Higress accepted the configuration. Save the caller-generated key before
sending. An authorized replay of the same deployment/key and normalized body
returns the original operation's current status, including terminal phases,
without reading a new catalog snapshot or re-enqueueing it. A different body
returns `409 IDEMPOTENCY_KEY_CONFLICT`. The binding and operation are retained
at least until `idempotencyExpiresAt`, which is at least 24 hours after
`createdAt`; replays do not extend it. Do not blindly retry an expired key: it
may create new work. After an uncertain response, retry the same key/body
within that window; once an operation ID is known, query it.

The queue stage retains operations and snapshots without automatic GC;
`idempotencyExpiresAt` is a minimum retention bound, not a deletion deadline.
Pending outbox entries are never removed by the existing retention cleaner.
Raw idempotency keys are not stored; only their SHA-256 hashes are persisted.

Relay delivery uses expiring claims, fenced acknowledgements, native retry
backoff and per-object sequence/resourceVersion guards. A newer full snapshot
supersedes older undelivered intents. A latest snapshot in terminal `failed`
state does not fall back to a superseded snapshot or retry automatically:
repair its inputs/ownership
and publish with a new key. Transient delivery errors still retry with backoff.
Removed models retain disabled ModelRoute
CR tombstones; they withdraw routing through the operator and fence delayed
creates. They are not physically deleted in this stage. Do not delete these
guards or mix direct spec writers with the relay. `projected` verifies these
CR writes, including disabled tombstones; it does not mean Higress is ready.
CRs converge individually; delivery is not an atomic multi-CR snapshot switch,
and the operator may observe intermediate configurations during delivery.
Relative upstream endpoints cannot be represented by ModelRoute and fail with
`INVALID_OPERATOR_SNAPSHOT`; the relay does not invent provider addresses.

`phase` describes delivery only: `queued` → `projecting` → `projected`,
`failed` or `superseded`. Transient delivery failures back off and retry;
`failed` is an unrecoverable projection error with a sanitized `errorCode`.
`projected` means the relay verified the snapshot's CR writes, including disabled
tombstones for removed routes.
A newer deployment publication can supersede an older one, including a
projected one; an older delayed worker must never overwrite newer intent.
`failed` and `superseded` are terminal. The relay's crash recovery, ordering
and fencing are covered by PostgreSQL and API Server acceptance tests.

The response always includes exactly one each of `Accepted`, `Applied` and
`Ready`, initially `Unknown`. Accepted is operator semantic validation;
Applied is rendered Kubernetes resources written; Ready needs independent
evidence that the intended revision is loaded in the data plane, with route
and relevant replica coverage. Each observation carries `observedRevision`;
missing/stale evidence has `null` and `Unknown`. A successful SSA, probe alone
or the legacy `ready` flag cannot establish Ready. Delivery can be projected
while validation fails, or Applied is True while Ready remains Unknown.
Conditions evolve independently of delivery phase. This stage does not add
a Ready detector or claim an end-to-end verified chain.

For the latest projected operation with the relay enabled, GET and POST replay
read operator evidence without claiming work or changing phases. Each CR must
match the immutable snapshot (including disabled tombstones), ownership,
publication sequence/operation/revision/hash and observed generation. Applied
also requires matching aggregate/route application digests. An annotation-only
republish does not increment generation: operators must record the exact
`status.observedPublication`, with optimistic status-write fencing. Install the
updated CRDs and operator before relying on these observations; older status
remains Unknown. A newer queued operation invalidates old evidence. Missing,
mixed or unavailable observations remain Unknown with sanitized reasons; Ready
always remains Unknown (`DataPlaneNotObserved`). Evidence is read on demand,
not stored as an application or billing history.

See the [publication OpenAPI source](../openapi/aep-v1-data-plane-publications.openapi.yaml)
and [SDK usage](../packages/aep-sdk-node/README.md#asynchronous-route-publication).

### Deployment settings

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/admin/deployment/settings` | Read deployment-level runtime settings |
| PUT | `/admin/deployment/settings` | Partially update runtime settings |

`GET` requires the `deployment.read` permission and `PUT` requires
`deployment.write`; the built-in administrator role carries both. Each setting
reports its stored `override`, the `effectiveValue` after environment
fallbacks, and the `source` of the effective value (`override`, `env`, or
`unset`).

The first managed setting is `modelGatewayBaseUrl`, the OpenAI-compatible model
gateway URL that service metadata advertises to clients. A `PUT` body of
`{"modelGatewayBaseUrl": "https://gateway.example.com/v1"}` stores a runtime
override that wins over `AEP_MODEL_GATEWAY_BASE_URL`; sending
`{"modelGatewayBaseUrl": null}` clears the override and restores the
environment value, and omitting the field leaves it unchanged. Values must be
absolute `http`/`https` URLs with a client-reachable hostname: cluster-internal
hosts (`*.svc.cluster.local` or single-label names) are rejected, and
production deployments additionally reject loopback addresses. Invalid values
return `422` with code `INVALID_DEPLOYMENT_SETTINGS`.

Three settings govern digital-employee compute. `computeDefaultTier` is the
default resource tier for employee runtime instances; a `PUT` value is
validated against the compute tier catalog at write time and unknown tier
names are rejected with `422`. `computeIdleHibernateSeconds` (60-86400) is the
idle duration before an employee runtime instance scales to zero, and
`computeConcurrencyCap` (1-10000) caps concurrent employee runtime instances
for the deployment. As with every setting, an explicit `null` clears the
override and restores the environment fallback (`AEP_COMPUTE_DEFAULT_TIER`,
`AEP_COMPUTE_IDLE_HIBERNATE_SECONDS`, `AEP_COMPUTE_CONCURRENCY_CAP`). Integer
settings report the same override/effectiveValue/source shape with integer
values.

### Compute governance

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/admin/compute/tiers` | Read the deployment-owned compute tier catalog |
| GET | `/admin/compute/assignments` | List per-agent compute tier assignments |
| PUT | `/admin/compute/assignments/{agentId}` | Set or clear one agent's compute tier override |

Both `GET`s require the `deployment.read` permission; the `PUT` requires
`agents.compute.write`, the delegable digital-employee compute grant that
cannot touch human accounts (full administrators always qualify).

Tiers are deployment-owned and read-only through the API: the control plane
loads the catalog from the JSON file named by `AEP_COMPUTE_TIERS_FILE` (an
object with an `items` array of tier entries: `name`, optional `description`,
`cpuRequest`/`cpuLimit`/`memoryRequest`/`memoryLimit` in Kubernetes quantity
syntax such as `250m` or `512Mi`, and `source` fixed to `deployment`). `GET
/admin/compute/tiers` returns an empty `items` array when no catalog file is
configured.

Each assignment row reports the agent, its stored tier `override`, the
`effective` tier after the deployment default tier, and the `source` of the
effective tier (`override` or `default`), plus `updatedAt` and `updatedBy`
when an override exists. A `PUT` body of `{"tier": "standard"}` stores the
override; `{"tier": null}` clears it so the deployment default tier applies.
The write is recorded in the deployment audit trail and takes effect at the
employee instance's next creation — v1 does not mutate running instances in
place. Unknown agents return `404`; tier names missing from the catalog return
`422`.

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

### Administrative audit

`GET /admin/audit/operations` requires `audit.read` and returns only records in
the caller's deployment, ordered by descending cursor. It accepts `cursor`,
`limit`, `action`, `resourceType`, `resourceId`, `actorUserId`, `createdAfter`,
and `createdBefore`; time filters use RFC 3339. The response contains `items`
and a nullable `nextCursor`.

Each record includes the actor, action, resource, result, timestamp, and
`changes`: masked before/after snapshots or a fallback request payload, or
`null` when no payload was recorded. Account-creation payloads retain the
username; passwords and unknown fields remain redacted. A URL field — a model
`endpoint` — keeps only its scheme, host, port, and path: the query, fragment,
and userinfo are removed both when the record is written and when it is read
back, so an inline credential never reaches the trail or a reader. This
read-only endpoint does not expose credential values. Records follow
`AEP_AUDIT_RETENTION`, as described in the [production runtime
guide](production-runtime.md).

Creating an account — and each account a bulk import creates — writes its audit
row in the same transaction as the account and its role/team bindings. An
account whose audit row cannot be written is not created: a single create fails
with `INTERNAL_ERROR`, and an import reports that row in `errors` and keeps the
rows that were audited. An import that ends up creating no account answers
`422 USER_IMPORT_REJECTED` and writes no success audit row, so the trail never
records an import that changed nothing.

Write attempts that were refused or failed are recorded too, with
`result: failure` and the stable problem code in `reason`. Coverage is every
authenticated non-read request (POST, PUT, PATCH, DELETE) that reaches the
administrative surface, including a request refused by the permission check:
the audit wraps that check, so a denial is captured with the acting user and the
attempted target. Requests refused before authentication (no bearer token,
invalid or revoked token, a temporary password that must be changed first) carry
no authenticated actor and stay the authentication audit's concern; reads are not
audited. The middleware stays best-effort: an audit write that fails is logged
and never changes the response the caller already received.

A failure record's `changes` document carries the request identifier and status
next to the masked attempted body, for example
`{"requestId":"0198...","status":400,"request":{"displayName":"..."}}`. The
masking rules are the same as for a successful write. Two rules keep this
bounded: a handler that committed its own audit row inside the transaction is
never duplicated by the fallback, and an identical repeated failure (same actor,
target, error code and masked body) is written once per 30 seconds per replica,
while a different attempt is always recorded.

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
`RESOURCE_NOT_FOUND`, `VERSION_CONFLICT`, `RATE_LIMITED`,
`INVALID_DEPLOYMENT_SETTINGS`, and `INTERNAL_ERROR`.

Retry safe reads with exponential backoff. Event batches are idempotent by
`eventId`. Do not blindly retry authorization-code exchange or credential
resolution. Honor `Retry-After` on `429` and `503`.

Team `defaultRoleIds` and effective role inheritance are documented in
[Team default roles](team-default-roles.md).

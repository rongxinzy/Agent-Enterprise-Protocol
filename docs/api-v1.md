# AEP v1 API Guide

[简体中文](api-v1.zh-CN.md) | English

This guide documents the AEP v1 HTTPS REST API. The machine-readable contract
is [`../openapi/aep-v1.openapi.yaml`](../openapi/aep-v1.openapi.yaml).

## 1. Conventions

Base URL: `https://enterprise.example.com/aep/v1`

Agent request headers:

```http
Authorization: Bearer <access-token>
X-AEP-Agent-ID: <stable-agent-instance-id>
X-AEP-Protocol-Version: 1.0
X-Request-ID: <request-id>
```

JSON properties use `camelCase`. Timestamps use RFC 3339 UTC. Errors use RFC
9457 `application/problem+json`. AEP v1 uses REST polling rather than SSE or
WebSocket.

## 2. Endpoints

### Service and authentication

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/metadata` | Supported AEP versions and features |
| POST | `/auth/exchange` | Exchange a one-time authorization code |
| POST | `/auth/refresh` | Refresh a session |
| POST | `/auth/logout` | Revoke the current refresh session |

### Agent API

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/agent/me` | Current user and enterprise |
| GET | `/agent/skills/manifest` | Complete desired Skill manifest |
| GET | `/agent/skills/{skillId}/versions/{version}/package` | Download Skill ZIP |
| POST | `/agent/skills/sync-results` | Report Skill synchronization |
| POST | `/agent/events/batch` | Upload an idempotent event batch |
| GET | `/agent/credentials` | List assigned credential metadata |
| POST | `/agent/credentials/{credentialId}/resolve` | Retrieve an Agent-deliverable secret |
| GET | `/agent/models` | List visible models |

## 3. Metadata and Authentication

### `GET /metadata`

```json
{
  "service": "zhiyuan-enterprise",
  "protocol": "aep",
  "minimumVersion": "1.0",
  "maximumVersion": "1.0",
  "features": ["skill-management", "event-upload", "credential-delivery", "model-catalog"]
}
```

### `POST /auth/exchange`

```json
{
  "authorizationCode": "one-time-code",
  "redirectUri": "zhiyuan://auth/callback",
  "agentId": "0198a910-5235-7b24-9b63-4b7dd46782e0",
  "agentVersion": "1.8.0",
  "platform": "windows"
}
```

```json
{
  "accessToken": "eyJ...",
  "refreshToken": "refresh-token",
  "tokenType": "Bearer",
  "expiresIn": 7200
}
```

The authorization code is single use.

### `POST /auth/refresh`

Request:

```json
{"refreshToken": "refresh-token", "agentId": "0198a910-5235-7b24-9b63-4b7dd46782e0"}
```

The response uses the token schema above. A returned refresh token replaces
the previous token.

### `POST /auth/logout`

Request: `{"refreshToken":"refresh-token"}`. Success returns `204 No Content`.

## 4. Current Identity

### `GET /agent/me`

```json
{
  "user": {"id": "user_123", "displayName": "Li Ming", "email": "liming@example.com"},
  "enterprise": {"id": "enterprise_001", "name": "Example Enterprise"},
  "roles": ["employee"],
  "sessionExpiresAt": "2026-08-19T10:00:00Z"
}
```

## 5. Skill Synchronization

### `GET /agent/skills/manifest`

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
      "url": "/aep/v1/agent/skills/docx/versions/1.3.0/package",
      "sha256": "8be72b3f1f47e36014fc8e1af54b250098201b1e2ea9a260153d69f7e64f1930",
      "size": 18342
    }
  }]
}
```

The response is complete. Managed Skills absent from it are removed.

### `GET /agent/skills/{skillId}/versions/{version}/package`

Returns `application/zip`. The server rechecks assignment, and the client
verifies the manifest SHA-256 before extraction.

### `POST /agent/skills/sync-results`

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

## 6. Event Upload

### `POST /agent/events/batch`

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

## 7. Credentials

### `GET /agent/credentials`

```json
{
  "credentials": [{
    "id": "credential_123",
    "name": "Internal Search API",
    "service": "internal-search",
    "deliveryMode": "agent",
    "maskedValue": "sk-****9f2a",
    "updatedAt": "2026-08-19T07:00:00Z"
  }]
}
```

### `POST /agent/credentials/{credentialId}/resolve`

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

## 8. Models

### `GET /agent/models`

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
The gateway enforces permission per request. AEP does not redefine inference
payloads.

## 9. Administration API

Administrative endpoints require an administrator identity.

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

Assignment example:

```json
{"skillId": "docx", "subject": {"type": "role", "id": "employee"}}
```

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
  "deliveryMode": "agent",
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

### Events

`GET /admin/events` supports `cursor`, `limit`, `userId`, `agentId`, `type`,
`resourceType`, `resourceId`, `result`, `occurredAfter`, and `occurredBefore`.

## 10. Errors and Retry

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

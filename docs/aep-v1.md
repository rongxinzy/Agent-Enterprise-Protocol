# Agent Enterprise Protocol v1

[简体中文](aep-v1.zh-CN.md) | English

Status: Initial Draft
Protocol version: `1.0`
Last updated: 2026-08-19

## 1. Purpose

Agent Enterprise Protocol (AEP) defines the REST API contract between an
enterprise-managed Agent client and enterprise services. It gives the Agent
SDK a stable integration boundary while customer-specific rules remain on the
server.

## 2. Scope

AEP v1 defines:

1. user identity and Agent session exchange;
2. managed Skill synchronization;
3. Agent telemetry event upload;
4. heartbeat-based discovery and reliable delivery of scoped control events;
5. API credential assignment and client delivery;
6. model catalog discovery and access decisions;
7. administrative APIs for these resources.

AEP v1 does not define model inference payloads, MCP invocation, Agent-to-Agent
collaboration, device attestation, signed policy artifacts, or
customer-specific business rules.

## 3. Participants

| Participant | Responsibility |
| --- | --- |
| Agent Client | Presents Agent features and applies managed state |
| Enterprise Agent SDK | Implements AEP in the Agent main process |
| Control Service | Owns identities, assignments, Skill metadata, and model permissions |
| Asset Service | Stores and serves Skill packages and optional artifacts |
| Event Service | Accepts telemetry and delivers scoped control events |
| Model Gateway | Enforces remote model permissions and invokes providers |
| Identity Provider | Authenticates users and supplies authorization codes |

Control, asset, and event services may be deployed as one modular service.

## 4. Client Boundary

For Electron, the AEP SDK MUST run in the main process. The renderer accesses
it through IPC.

```text
Renderer UI
    | IPC
    v
Electron Main Process / Enterprise Agent SDK
    | HTTPS REST API
    v
Enterprise Services
```

Tokens and client-deliverable credentials MUST NOT be exposed to the renderer.
Provider credentials used by a gateway MUST remain on the server.

## 5. REST Transport

AEP data exchange uses REST APIs over HTTPS.

- Production deployments MUST use HTTPS.
- Resources are identified by URLs under `/aep/v1`.
- `GET` reads resources and MUST NOT change business state.
- `POST` creates resources or executes explicit commands.
- `PATCH` partially updates resources.
- `DELETE` withdraws or deletes resources.
- JSON uses `application/json` with UTF-8.
- Errors use RFC 9457 `application/problem+json`.
- Skill packages use `application/zip`.
- Timestamps use RFC 3339 UTC.
- Identifiers are opaque strings.

AEP v1 does not use SSE, WebSocket, or a custom persistent connection.
Clients discover changes through heartbeat flags, control-event polling, and
conditional resource polling. Model streaming may use the model API's own
protocol and is outside AEP.

## 6. Request Headers

Authenticated Agent requests include:

```http
Authorization: Bearer <access-token>
X-AEP-Agent-ID: <stable-agent-instance-id>
X-AEP-Protocol-Version: 1.0
X-Request-ID: <request-id>
```

`X-AEP-Agent-ID` identifies an installation but is not a credential. The
server derives the user and enterprise from the access token. Servers SHOULD
echo `X-Request-ID` or create one when absent.

Conditional requests use standard HTTP headers such as `ETag` and
`If-None-Match`.

## 7. Authentication

The normal login sequence is:

```text
Agent opens enterprise login page
  -> Identity Provider authenticates user
  -> Agent receives one-time authorization code
  -> Agent exchanges code through REST API
  -> Server returns access and refresh tokens
```

Access tokens authenticate normal requests. Refresh tokens are used only at
the refresh endpoint. Logout revokes the refresh session. The current-identity
endpoint is the canonical source for displayed user and enterprise data.

## 8. Skill Synchronization

### 8.1 Desired Manifest

The server returns the complete desired Skill manifest for the authenticated
user. Each item contains a stable ID, version, package URL, SHA-256 digest,
size, and enablement state.

The response includes an opaque revision and `ETag`. The Agent polls with
`If-None-Match`; `304 Not Modified` means no reconciliation is required.

### 8.2 Reconciliation

When the manifest changes, the Agent:

1. downloads missing or changed packages;
2. verifies the SHA-256 digest;
3. installs packages in the managed Skill directory;
4. removes managed Skills absent from the complete manifest;
5. refreshes the runtime Skill list;
6. reports the result through REST API.

Only AEP-managed Skills are reconciled. The Agent MUST NOT delete unmanaged
Skills or unrelated user files.

### 8.3 Package Format

A Skill package is a ZIP archive containing `SKILL.md` at its root. It may
also contain scripts, references, and assets. Paths MUST be relative and MUST
NOT escape the extraction directory. The manifest digest is calculated over
the exact ZIP bytes served by the asset service.

### 8.4 Removal Semantics

- Unassigning a Skill removes it from that subject's desired manifest.
- Withdrawing a version prevents future downloads of that version.
- Deleting a Skill removes it from future manifests.
- Previously delivered unmanaged copies cannot be guaranteed to disappear.

## 9. Events

AEP distinguishes two event directions:

| Direction | Name | Purpose |
| --- | --- | --- |
| Agent to server | Telemetry event | Report Agent activity and outcomes |
| Server to Agent | Control event | Notify an Agent that managed state or a task changed |

### 9.1 Telemetry Upload

Agents upload telemetry events in REST batches. Every event has a globally
unique `eventId`; the server deduplicates by this value, allowing safe retries.

A batch contains at most 100 events and SHOULD remain below 1 MiB. The server
returns accepted and rejected event IDs. The Agent retains retryable failures
in a local outbox.

Standard telemetry event types include `auth.login`, `auth.logout`,
`skill.sync.started`, `skill.installed`, `skill.updated`, `skill.removed`,
`skill.sync.failed`, `credential.resolved`, `credential.resolve_failed`,
`model.request.completed`, and `model.request.failed`.

Telemetry metadata MUST NOT contain tokens, API keys, or complete model
prompts and responses unless a separate enterprise policy explicitly enables it.

### 9.2 Control Event Scope

Control events use one of four scopes: `global`, `organization`, `user`, or
`agent`. The server derives applicable scopes from the authenticated identity
and registered Agent instance. A client MUST NOT select its own organization
or user scope.

A global, organization, or user event has an independent delivery state for
every applicable Agent. There is no shared `consumed` flag on the event itself.

### 9.3 Discovery and Delivery

The Agent sends a REST heartbeat. The response contains only a pending flag
and server watermark. When the flag is true, the Agent queries the control
event endpoint.

Reading an event does not consume it. The Agent first writes the event to a
durable local inbox, then acknowledges it as `received`. The server redelivers
unacknowledged events. After execution, the Agent reports `succeeded` or
`failed` independently from receipt acknowledgement.

Delivery states are `pending`, `received`, `running`, `succeeded`, `failed`,
`expired`, and `superseded`. Acknowledgement and result requests are idempotent.

### 9.4 Event as Invalidation Signal

A control event SHOULD be a small invalidation signal rather than the source
of managed data. For example, `skill.manifest.changed` instructs the Agent to
retrieve the latest Skill manifest and reconcile it. Secrets, complete Skill
packages, and large configuration objects MUST NOT be embedded in an event.

Standard mappings include:

| Event type | Task |
| --- | --- |
| `skill.manifest.changed` | Pull and reconcile the Skill manifest |
| `plugin.manifest.changed` | Pull and reconcile the plugin manifest when supported |
| `credential.assignments.changed` | Refresh credential assignments |
| `model.catalog.changed` | Refresh the model catalog |

The detailed REST contract is defined in the API guide and the Control Events
OpenAPI document.

## 10. API Credentials

AEP defines two delivery modes:

| Mode | Behavior |
| --- | --- |
| `server_only` | Secret stays on the server and is used by a gateway |
| `agent` | An authorized Agent may retrieve the secret through REST API |

Model-provider keys SHOULD be `server_only`. Credential list responses expose
metadata and masked values only. Secret material is returned only by the
resolve endpoint with `Cache-Control: no-store`.

Disabling or deleting a credential blocks future resolution. A server cannot
guarantee deletion of a secret already delivered to an untrusted client.

## 11. Models and Permissions

The model catalog contains only models visible to the current user. A model
descriptor contains its ID, display name, source type, invocation protocol,
endpoint or local reference, capabilities, and enablement state.

Source types are:

| Source type | Meaning |
| --- | --- |
| `gateway` | Commercial or hosted model reached through the model gateway |
| `enterprise_open_source` | Enterprise-hosted open-source model |
| `local` | Model executed on the Agent device |

The model gateway MUST enforce remote permissions again on every inference
request. Catalog filtering alone is insufficient. Local-model permissions are
product controls and cannot resist a user who controls the machine.

AEP does not redefine inference data. The descriptor's `protocol` tells the
SDK which model client to use.

## 12. Administration

Administration APIs manage Skills and versions, user or role assignments,
credentials and rotation, model descriptors and assignments, and event
search. They require an administrator identity distinct from ordinary Agent
authorization.

## 13. Error Model

Errors follow RFC 9457:

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

Standard codes include `INVALID_REQUEST`, `TOKEN_INVALID`, `ACCESS_DENIED`,
`RESOURCE_NOT_FOUND`, `VERSION_CONFLICT`, `RATE_LIMITED`,
`SKILL_NOT_ASSIGNED`, `CREDENTIAL_NOT_DELIVERABLE`, `MODEL_NOT_ALLOWED`, and
`INTERNAL_ERROR`.

## 14. Compatibility

The major version appears in the base path. Additive fields and endpoints do
not change the major version. Clients MUST ignore unknown JSON properties.
Breaking changes require a new path such as `/aep/v2`.

The metadata endpoint advertises the supported version range. Unsupported
clients receive `426 Upgrade Required` with AEP Problem Details.

## 15. Minimum Security Baseline

AEP v1 does not require signed policy artifacts or device attestation, but it
does require:

- HTTPS outside local development;
- authorization based on authenticated identity;
- authorization on every package download, credential resolution, and remote
  model request;
- encrypted storage for retrievable secrets;
- secret redaction in logs and events;
- archive path validation before Skill extraction;
- request and event size limits;
- refresh-session revocation on logout or account disablement.

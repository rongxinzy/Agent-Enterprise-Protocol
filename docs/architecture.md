# Architecture

AEP separates two API surfaces that scale, trust, and fail independently:

| Surface | Served by | Consumers | Contents |
| --- | --- | --- | --- |
| **Agent control protocol** | `aep-agent-control` (split) / mounted by the control-service (all-in-one) | Desktop agents (the Zhiyuan enterprise extension), the reference Node agent | `/aep/v1/auth/*`, `/aep/v1/user/*` (heartbeat, control-event inbox, skills, telemetry, model connection, credential resolution), JWKS, metadata |
| **Enterprise management** | `aep-control-service` | Admin Console, governance portal, DeerFlow governance middleware, digital-employee service, gateway data plane | `/aep/v1/admin/*`, `/internal/*`, JWKS, metadata |

The protocol spec (`aep-v1.md`) names the Control, Event, and Asset participants and allows deploying them as one modular service; the agent-control binary actualizes that participant as its own process.

## Deployment shapes

- **All-in-one** (default; compose, local k3s, tests): the control-service mounts every route. Nothing advertises `agentControl.baseUrl`; clients use one base URL.
- **Split** (production k8s): `aep-agent-control` runs beside the control-service. Ingress routes `/aep/v1/user`, `/aep/v1/auth`, and `/.well-known` to it; the control-service's ingress tightens to management callers. Metadata advertises `agentControl.baseUrl` (env `AEP_AGENT_CONTROL_BASE_URL`, overridable at runtime through deployment settings) so desktop agents discover the endpoint without configuration.

## Shared state (the split is a process boundary, not a data boundary)

- **PostgreSQL** — sessions, control events, skills, telemetry, credentials. Access-token verification re-checks the session row per request, so both services validate sessions directly against the shared database.
- **MinIO** — the `aep-skills` bucket; the agent surface streams package downloads from the same object keys the admin surface uploads.
- **Signing key** — one Ed25519 seed issues access, model, and entitlement tokens on either service; JWKS is served by both. Same trust domain; the gateway data plane already verifies model tokens via JWKS without calling either service.
- **Credential keyring** — the AEAD master key; the agent surface decrypts resolved credentials with the same keyring the admin surface encrypts with.

Retention, migrations, and bootstrap stay with the control-service (`app.Open` is idempotent — advisory-locked migrations — so a racing agent-control boot is harmless).

## Network policy (production)

The agent-control ingress is the widest control-plane policy: desktop-agent runtime traffic (heartbeats, event polling, skill downloads) arrives through the shared higress ingress. The control-service accepts traffic only from the gateway authorizer, the gateway reconciler, and the ingress namespace. Both egress to DNS, 443 (MinIO/external), 5432 (PostgreSQL), 9000 (MinIO).

## Client routing

The Node SDK's `AepClient` accepts an optional `agentControlBaseUrl`: `/aep/v1/auth/*` and `/aep/v1/user/*` requests travel to it, everything else to `baseUrl`. Omitted, all traffic uses one host — the all-in-one shape. Clients can learn the split endpoint from service metadata (`agentControl.baseUrl`), mirroring the model-gateway discovery pattern.

## In-repo layout

- `services/control-service/cmd/server` — enterprise binary (all-in-one mounts everything).
- `services/control-service/cmd/agent-control` — agent control protocol binary; shares the internal packages by design (the code boundary is the route-group constructors in `internal/httpapi`: `New`, `NewEnterpriseAPI`, `NewAgentControlAPI`).
- `openapi/aep-v1-agent.openapi.yaml` — the agent contract bundle (auth + user runtime surface).

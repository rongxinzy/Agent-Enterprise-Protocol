# AEP v1 Foundation Release Readiness

Status: release candidate
Implementation profile: M3
Measured foundation completion: 90%
Assessment date: 2026-08-24

## Release Decision

The enterprise foundation is ready for independent integration testing and a controlled enterprise pilot. It is not a general-availability release.

A real product client is not required to validate this 90% foundation gate. The Node reference Agent exercises the same SDK, session, Skill, event, Credential, and model paths expected from a real client. A RongxinAI main-process integration and a customer identity pilot remain part of the final 10%.

The authoritative machine-readable score is in release/foundation-readiness.json. The scripts/release-audit.mjs check rejects score drift, missing evidence, mismatched toolchain or protocol versions, unsafe production defaults, and removal of required CI gates.

## Score

| Gate | Weight | Result |
| --- | ---: | --- |
| Contract and Node SDK Core | 15% | Complete |
| Platform identity and session lifecycle | 15% | Complete |
| Skill control loop | 10% | Complete |
| Control events and telemetry | 10% | Complete |
| Model control plane and Higress data plane | 15% | Complete |
| Credential control and Agent delivery | 10% | Complete |
| Reference Agent | 5% | Complete |
| Production runtime baseline | 5% | Complete |
| Customer client and IdP integration | 5% | Pending |
| Production data-plane automation | 5% | Complete |
| GA validation and release supply chain | 5% | Pending |

Completed weight is 90 of 100. Pending work is not counted as partial completion.

## Available Release Surface

Administrators can use aepctl to manage users, Skills, scoped assignments, control events, models, Credentials, Agents, deliveries, and audit records. The Node SDK provides authentication, refresh rotation, RFC 9457 errors, safe retry behavior, all implemented Agent and administration APIs, model connection discovery, and Credential no-store enforcement.

The reference Agent persists event inbox and telemetry outbox state, safely reconciles Skill packages, converges after restart, consumes authorized models through the Higress path, and resolves short-lived Credential material without writing it to SQLite, telemetry, logs, or command output.

The control service and gateway authorizer expose liveness, dependency-aware readiness, structured logs, bounded request handling, and Prometheus metrics. Production configuration fails closed on development database, object-store, signing, administrator-password, and mock-federated-auth settings.

## Security And Compatibility Gates

Mock OIDC is a development and test fixture. It defaults off in production, production configuration rejects attempts to enable it, authentication discovery omits it, and metadata does not advertise federated_auth. A real enterprise OIDC adapter must preserve Authorization Code with PKCE, one-time exchange, identity mapping, and the same AEP session contract.

Metadata remains accessible without a protocol header for discovery. Every other /aep/v1 request requires X-AEP-Protocol-Version: 1.0. Missing or unsupported versions receive RFC 9457 status 426 and X-AEP-Supported-Protocol-Versions.

Higress standalone and the static AI Proxy mapping remain local and CI fixtures. Production Higress must use Helm, protected provider Secrets, TLS ingress, and organization-specific availability controls.

The gateway reconciler now applies tenant Ingress and Higress WasmPlugin resources through Kubernetes server-side apply. Deterministic names, one field manager, periodic drift repair, delete-on-disable behavior, partial-failure status, and two-replica convergence are covered by fake-control-plane fault tests and a real kind API Server gate.

## Remaining 10%

Customer client and IdP integration, 5%: connect a real enterprise OIDC or customer adapter and complete a RongxinAI main-process pilot. Tokens and Credential values must remain outside the renderer.

GA validation, 5%: complete load and soak testing, external security review, backup and disaster-recovery rehearsal, signed artifacts and SBOM publication, and remaining draft-profile cleanup. The M3 profile does not yet expose single Skill-version withdrawal; administrators can revoke assignments, disable or delete the Skill until that lifecycle operation is added.

## Go Or No-Go

Run the complete local release gate:

~~~sh
npm run release:check
~~~

The command validates OpenAPI and generated SDK types, builds and tests Node workspaces, validates the readiness score, runs Go test, race, vet, and build gates, and executes every Compose E2E scenario. CI must finish with sdk-gate, control-service, example-agent, compose-e2e, data-plane-kubernetes, and release-gate all successful.

A production pilot is no-go if any production default is reused, mock federated authentication is enabled, HTTPS or equivalent trusted transport is absent on an untrusted network, backup ownership is undefined, or the target Higress topology is based on higress-standalone.

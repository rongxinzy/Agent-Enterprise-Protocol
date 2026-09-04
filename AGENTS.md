# Agent Enterprise Protocol Developer Guide

This repository is the public implementation of the Agent Enterprise Protocol
(AEP). It contains the OpenAPI contract, Node SDK, Go control-plane services,
administrative CLI, reference Agent, gateway components, deployment manifests,
and contract/E2E tests. It does not contain the closed Zhiyuan enterprise
extension, customer configuration, License signer, or production secrets.

Use this file as the repository-specific entry point before changing code.

## Scope and Boundaries

- `openapi/` is the protocol source of truth and generated bundle output.
- `packages/aep-sdk-node/` is the public TypeScript SDK for control-plane
  communication and session lifecycle. It does not own local Agent inboxes,
  Skill installation, or model inference.
- `services/control-service/` is the modular Go control-plane monolith for
  identity, sessions, authorization, Skills, events, telemetry, Credentials,
  and model catalog data.
- `services/gateway-authorizer/` validates AEP model JWTs and model scope before
  forwarding an OpenAI-compatible request.
- `services/gateway-reconciler/` renders approved data-plane desired state and
  applies it to Kubernetes/Higress.
- `cmd/aepctl/` is the administrator CLI and must use the public management API;
  it must not access the database directly.
- `examples/node-agent/` is the reference client used for SDK and E2E coverage.
- `tests/contract/` and `tests/e2e/` define compatibility and end-to-end gates.
- `deploy/compose/` is local/CI development infrastructure. Production uses
  the Kubernetes baseline under `deploy/kubernetes/production/` with external
  Secret, database, object storage, TLS, and monitoring systems.

The AEP repository may document the public integration contract, but must not
copy enterprise source from the closed AaaS repository or add enterprise UI,
License activation logic, signing keys, or customer-specific policy.

## Toolchain and Commands

- Node.js `>=24 <25`, npm, Go `1.26`, and Docker Compose v2 are required.
- Use the checked-in `package-lock.json` and run `npm ci` for a clean install.
- `gh` commands must run from Git Bash on Windows, not PowerShell.

Fast local checks:

```bash
npm ci
npm run openapi:lint
npm run openapi:build
npm run build
npm test
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

The complete release gate is:

```bash
npm run release:check
```

This includes OpenAPI drift checks, Node workspace tests, SDK package checks,
Go tests/race/vet/build, release audit, and every Compose E2E scenario.

## Contract-First Development

For a new protocol feature, follow this order:

1. Update the appropriate source OpenAPI document under `openapi/` and its
   English/Chinese API documentation.
2. Run `npm run openapi:lint` and `npm run openapi:build`.
3. Implement or update SDK types and public methods in
   `packages/aep-sdk-node/`, including Mock contract tests.
4. Keep generated bundles and `src/generated/aep-v1.ts` in sync. Generated
   files are outputs, not places for hand edits.
5. Only after the SDK contract gate passes, implement the Go handlers,
   persistence, and service behavior.
6. Add or update reference-Agent and E2E coverage for the observable contract.

SDK and service work may be developed in separate PRs, but a new API contract
must not be implemented server-first or by inventing a client-only schema.
Preserve backwards compatibility unless the protocol explicitly permits a
breaking change.

## API and Security Invariants

- Versioned `/aep/v1` requests require `X-AEP-Protocol-Version: 1.0`; metadata
  and health endpoints are the exceptions documented by the API guide.
- HTTP errors use RFC 9457 `application/problem+json`, stable problem codes,
  and a request ID. Do not leak database, provider, credential, or token data.
- Passwords use Argon2id. Refresh tokens are stored hashed, rotated on refresh,
  and revoked on logout or account disablement.
- AEP access and model JWTs are short-lived and signed with Ed25519. JWKS is the
  public verification surface; signing seeds are deployment secrets.
- Provider API keys and Credential values stay in server-side Secret storage or
  the protected delivery path. Never place them in model descriptors, Agent
  logs, telemetry, API responses, fixtures, or command output.
- Logs must redact passwords, bearer tokens, refresh tokens, API keys, resolved
  Credential values, and request bodies containing secrets.
- Treat all client-provided tenant, user, Agent, model, Skill, and scope values
  as untrusted. Derive authorization from the authenticated session and stored
  assignments.
- Database migrations are versioned and forward-only. Use GORM repositories
  for ordinary runtime persistence, keep complex lock-sensitive operations as
  explicit PostgreSQL queries, and preserve advisory-lock startup behavior.
  Production code must never call `AutoMigrate`; schema changes belong in the
  reviewed SQL migrations. Do not edit generated sqlc files by hand while
  transitional sqlc queries remain.

## Control Plane and Data Plane

AEP is a management protocol, not a replacement for model inference, MCP, or
Agent-to-Agent protocols. Keep the two model paths distinct:

```text
Agent -- AEP control/session API --> Control Service
Agent -- model JWT --> gateway-authorizer --> Higress --> provider
```

The authorizer must validate issuer, audience, token use, time claims, identity
claims, and requested model membership in `model_scopes` before forwarding.
Higress injects the provider credential server-side; the client must never see
it. The local static Higress mapping is a test fixture, not a production
topology. Production requires the Kubernetes/Helm baseline, TLS, Secret
management, and an approved availability design.

## Local Stack and E2E

Start the control stack before running a reference Agent or manually exercising
the gateway:

```bash
npm run compose:up
# gateway profile, when model data-plane testing is needed:
npm run compose:gateway:up
```

Default development endpoints are Control Service `http://localhost:8080`,
MinIO console `http://localhost:9001`, and model gateway
`http://localhost:8090/v1` when the gateway profile is enabled. Development
bootstrap credentials and mock provider keys are disposable fixtures only.
Change them before exposing any service outside the local machine.

Run focused scenarios before the full gate:

```bash
npm run test:e2e:m0
npm run test:e2e:m1-control
npm run test:e2e:m1-gateway
npm run test:e2e:m1-client
npm run test:e2e:m2-control
npm run test:e2e:m2-agent
npm run test:e2e:runtime
npm run test:e2e:m3-data-plane
npm run test:e2e:m3-kubernetes
```

E2E scripts use isolated Compose project names and clean up their own
containers/volumes. For failures, inspect the scoped project with
`docker compose ... ps` and `docker compose ... logs <service>` before changing
the test or deleting data.

## SDK and Reference Agent Rules

- `AepClient`, `AepTokenStore`, `AepTransport`, and `AepProblem` are public SDK
  surfaces; keep transport/session concerns separate from Agent business state.
- Implement request headers, timeout bounds, safe idempotent retry, RFC 9457
  mapping, refresh single-flight, and refresh-failure cleanup consistently.
- The reference Agent stores events before acknowledgement, makes event and
  telemetry operations idempotent, resumes after restart, and rejects Skill
  ZIP traversal, symbolic links, size violations, and SHA-256 mismatches.
- Tool-call continuation must preserve the prior assistant reasoning content
  when the provider contract requires it. Reasoning behavior is model metadata,
  not a model-name string heuristic.
- Model calls remain direct OpenAI-compatible requests using the model access
  token. Do not tunnel inference through SDK control APIs.

## Deployment and Operations

Local Compose is for development and CI only. Production manifests must source
database URLs, object-store credentials, signing material, data-plane tokens,
provider credentials, and bootstrap secrets from the deployment Secret system.
Do not commit rendered `Secret` objects or customer overlays.

Before changing production runtime behavior, review:

- `docs/production-runtime.zh-CN.md` for startup, probes, migration, backup,
  recovery, and log requirements;
- `docs/production-data-plane.zh-CN.md` for Kubernetes/Higress reconciliation;
- `docs/release-readiness.zh-CN.md` for release gates and remaining risk.

Readiness and liveness are separate contracts. Do not make a dependency outage
look healthy, and do not make a healthy process fail liveness merely because a
downstream database or provider is unavailable.

## Change and PR Rules

- Keep PRs stage-oriented: contract/OpenAPI, SDK, control service, gateway,
  CLI, reference Agent, deployment, or test/release verification.
- One coherent behavior change per PR. Do not mix enterprise AaaS changes or
  public Zhiyuan UI changes into this repository.
- Commit messages and PR titles use Conventional Commits.
- Use `apply_patch` for manual edits. Update generated artifacts only through
  their owning command.
- Before opening a PR, run `git diff --check`, the narrow relevant tests, and
  inspect `git status` for unrelated user changes. Never discard existing work.
- A release PR must include the applicable OpenAPI drift, SDK package, Go
  security, Compose E2E, deployment, and release-audit evidence.

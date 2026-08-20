# AEP M0 Runbook

[Simplified Chinese](m0-runbook.zh-CN.md) | English

## Scope

M0 provides the independently runnable enterprise-control loop: `aepctl`
manages users and Skills, the Node example Agent authenticates through the SDK,
reconciles managed Skills, processes durable control events, reports telemetry,
and exposes the resulting state to administrators.

M0 does not provide a production model gateway, model calls, Credential, MCP,
Plugin, A2A, DLP, quota, or policy-artifact delivery. The authentication response
includes a verifiable model access JWT, but no M0 component consumes it.

## Prerequisites

- Node.js 24 and npm
- Go 1.26
- Docker with Compose v2
- Free local ports `8080` and `9001`, or custom `AEP_PORT` and
  `AEP_MINIO_CONSOLE_PORT` values

Install dependencies and run the non-container checks:

```bash
npm ci
npm run check
go test ./...
go vet ./...
go build ./...
```

`npm run check` lints and bundles OpenAPI, regenerates SDK types, builds every
Node workspace, and runs the SDK and example-Agent tests.

## Start The Stack

```bash
npm run compose:up
```

The default local endpoints are:

- control service: `http://localhost:8080`
- MinIO console: `http://localhost:9001`
- health check: `http://localhost:8080/healthz`

The local-only bootstrap identity is enterprise `demo`, user `admin`, password
`change-this-admin-password`. Compose also contains fixed development signing and
MinIO credentials. Change all of these before exposing the service outside a
developer workstation.

To use a different service port, set `AEP_PORT` before starting Compose. Set
`AEP_MINIO_CONSOLE_PORT` similarly for the MinIO console. Set `GOPROXY` to
override the Go module proxy used by the image build.

## Manage M0

Prefer `AEPCTL_PASSWORD` over the `--password` flag outside a disposable demo.
The examples below show the flag to remain shell-independent.

```bash
go run ./cmd/aepctl --password change-this-admin-password metadata
go run ./cmd/aepctl --password change-this-admin-password user create --user agent-user --display-name "Agent User" --temporary-password change-this-user-password --require-password-change=false
go run ./cmd/aepctl --password change-this-admin-password user list
```

Create a ZIP whose root contains `SKILL.md`, then use the returned user ID and
assignment ID in the following commands:

```bash
go run ./cmd/aepctl --password change-this-admin-password skill create --skill-id review --name Review --description "Managed review Skill"
go run ./cmd/aepctl --password change-this-admin-password skill upload --skill-id review --version 1.0.0 --file ./review.zip
go run ./cmd/aepctl --password change-this-admin-password skill publish --skill-id review --version 1.0.0
go run ./cmd/aepctl --password change-this-admin-password skill assign --skill-id review --subject-type user --subject-id USER_ID
go run ./cmd/aepctl --password change-this-admin-password event publish --scope-type agent --scope-id AGENT_ID --skill-id review --revision 1
```

The example Agent reads these environment variables: `AEP_BASE_URL`,
`AEP_ENTERPRISE_ID`, `AEP_USERNAME`, `AEP_PASSWORD`, `AEP_AGENT_ID`, and
optionally `AEP_AGENT_DATA_DIR`. After `npm run build`, run one reconciliation
cycle with:

```bash
node examples/node-agent/dist/index.js once
```

Inspect the resulting records:

```bash
go run ./cmd/aepctl --password change-this-admin-password agent show --agent-id AGENT_ID
go run ./cmd/aepctl --password change-this-admin-password event deliveries --event-id EVENT_ID
go run ./cmd/aepctl --password change-this-admin-password audit --agent-id AGENT_ID
```

## End-To-End Acceptance

```bash
npm run test:e2e
```

The script uses isolated project name `aep-m0-e2e`, service port `18080`, and
MinIO console port `19001`. It creates the user, Skill, version, publication, and
assignment through `aepctl`; runs the Agent; verifies install and revocation;
checks delivery, Agent, and telemetry records; and removes only its own containers
and volumes. Override the two host ports with `AEP_E2E_PORT` and
`AEP_E2E_MINIO_CONSOLE_PORT`.

For diagnosis, run `docker compose -f deploy/compose/compose.yaml ps` and
`docker compose -f deploy/compose/compose.yaml logs control-service`. Stop the
manual stack with `npm run compose:down`; its named data volumes are preserved.

## Release Checklist

- OpenAPI lint and bundle pass, generated SDK types have no diff.
- SDK build and Mock contract tests pass before Go jobs start.
- Example Agent build, SQLite recovery tests, ZIP traversal rejection, and hash
  validation pass.
- `go test ./...`, `go vet ./...`, and `go build ./...` pass.
- Compose E2E passes with PostgreSQL and MinIO and cleans its scoped resources.
- Bootstrap passwords, signing key, issuer, MinIO credentials, and HTTP exposure
  are reviewed for the target environment.
- M0 capability metadata does not advertise unsupported gateway, Credential,
  MCP, or Plugin features.

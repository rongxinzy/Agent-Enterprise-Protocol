# AEP Production Runtime Baseline

This baseline makes the AEP control service and gateway authorizer operable under a production orchestrator. It does not turn the local Compose stack or `higress-standalone` into a production topology. Production still requires externally managed PostgreSQL and S3-compatible MinIO, Higress Helm deployment, TLS ingress, Secret management, monitoring, backups, and an organization-specific availability design.

## Configuration Gate

Set `AEP_ENVIRONMENT=production`. The control service then refuses to start with an ephemeral JWT signing key, the development PostgreSQL URL, default MinIO credentials, or the default/short bootstrap administrator password, and requires a License trusted-key file, License file, customer ID, and deployment ID. Startup verifies the mounted License and refuses invalid or expired licenses. Invalid booleans, durations, URLs, log settings, request limits, and header limits always fail startup instead of silently reverting to defaults.

Mock federated authentication is a development and test fixture. Production defaults it off and rejects AEP_ENABLE_MOCK_FEDERATED_AUTH=true. Do not advertise or expose federated_auth until a real enterprise identity adapter is configured.

Use [control-service.env.example](../deploy/production/control-service.env.example) and [gateway-authorizer.env.example](../deploy/production/gateway-authorizer.env.example) as deployment inputs. Sensitive values support `VARIABLE_FILE` paths. A direct value and its `_FILE` form are mutually exclusive. The Credential keyring continues to use `AEP_CREDENTIAL_MASTER_KEY_FILE` so old decryption keys can remain available during controlled rotation. The data-plane reconciler token also supports the file form.

The signing seed, Credential keyring, database credentials, object-store credentials, and bootstrap password must come from the orchestrator's Secret provider. Do not place them in images, ConfigMaps, Git, Helm values, or shell history.

## Password Authentication

Passwords must contain 12 to 1024 Unicode characters and are stored with Argon2id. Temporary-password sessions are server-restricted until the user changes the password; their model token carries no model scopes. Login failures are tracked in PostgreSQL by an opaque principal hash so the progressive backoff is shared across sources and control-service replicas; the source is retained only as a separate opaque audit hash. Configure `AEP_LOGIN_FAILURE_LIMIT`, `AEP_LOGIN_FAILURE_WINDOW`, `AEP_LOGIN_BACKOFF_BASE`, and `AEP_LOGIN_BACKOFF_MAX` for the deployment threat model.

Authentication audit rows contain enterprise and Agent identifiers plus opaque principal/source hashes, but never usernames, passwords, tokens, or request bodies. Apply an organization-approved retention policy to `authentication_audit_events`. A password reset or account disable revokes all refresh sessions; already issued access and model JWTs expire at their configured short TTL.

## Runtime Endpoints

| Endpoint | Meaning | Orchestrator use |
| --- | --- | --- |
| `/livez` | Process can serve HTTP; no dependency check | Liveness probe |
| `/readyz` | Control: PostgreSQL and MinIO ready. Gateway: trusted JWKS refresh succeeds | Readiness probe |
| `/healthz` | Backward-compatible alias of `/readyz` | Existing integrations |
| `/metrics` | Prometheus/OpenMetrics with stable route, method, status, latency, and in-flight requests | Internal metrics scrape |

Metrics never label by tenant, user, Agent, resource ID, request ID, query string, or token. Access logs contain request ID, method, stable route, status, response bytes, and duration only. Production defaults to JSON logs. Authorization headers, bodies, query strings, Credential values, and model prompts are not logged.

Both distroless images expose an internal probe command:

```sh
/aep-control healthcheck http://127.0.0.1:8080/readyz
/aep-gateway-authorizer healthcheck http://127.0.0.1:8090/readyz
```

## Availability And Rollout

Migration execution and bootstrap administrator initialization are protected by PostgreSQL advisory locks. Multiple control-service replicas may start against a new or upgraded database without racing schema or bootstrap writes. MinIO bucket initialization also tolerates concurrent first creation.

Use at least two control-service and two gateway-authorizer replicas across failure domains when the dependent services meet the same availability target. During rollout:

1. Back up PostgreSQL, the Skill bucket, the Ed25519 signing seed, and the complete Credential keyring.
2. Start one new control-service replica and wait for `/readyz`.
3. Roll remaining control-service replicas, then gateway-authorizer replicas.
4. Verify error rate and latency metrics, Agent login, Credential resolve, Skill download, and model calls.
5. Retain the previous image digest until the observation window closes.

Database migrations are forward-only. Application rollback is allowed only when the previous binary supports the migrated schema. Otherwise restore PostgreSQL and MinIO from the coordinated pre-rollout backup, then restore the matching signing seed and Credential keyring.

## Backup And Recovery

Use a maintenance window or coordinated storage snapshots so PostgreSQL and the Skill bucket represent one recovery point. PostgreSQL contains identities, assignments, encrypted Credential material, events, audit, and object references; MinIO contains immutable Skill ZIP objects. Back up the signing seed and every Credential keyring entry separately through the Secret system. Losing an old Credential key makes its rows undecryptable.

Restore into isolated PostgreSQL and MinIO instances first, verify object counts and database integrity, then start exactly one control-service replica to run embedded migrations. Confirm `/readyz`, JWKS continuity, a Credential resolve audit, and a Skill checksum before adding replicas or switching traffic.

## Verification

```sh
npm run test:e2e:runtime
```

The scenario validates concurrent first startup, dependency-aware readiness, independent liveness, Prometheus metrics, structured logs, container hardening, and clean SIGTERM exit. The complete release gate remains `npm run test:e2e`.

The Kubernetes, Higress Helm, TLS, RBAC, External Secrets, and live data-plane reconciliation baseline is documented in [production-data-plane.md](production-data-plane.md).

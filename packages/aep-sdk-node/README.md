# AEP Node SDK

[简体中文](README.zh-CN.md)

`@aep/sdk-node` is the Node.js communication and session SDK for Agent Enterprise Protocol v1. It supports Node.js 24 and publishes ESM, CommonJS, and TypeScript declarations.

The SDK owns protocol headers, RFC 9457 errors, bounded retries, token refresh rotation, and AEP API calls. Agent inbox/outbox persistence, Skill installation, model inference, and application UI remain the responsibility of the product client.

## Install

Install a fixed release version. Do not depend on a moving Git branch or a local workspace path in a product build.

```sh
npm install @aep/sdk-node@0.2.9
```

For air-gapped or tarball installs, use the tarball and SHA-256 file attached to the matching `sdk-node-v0.2.9` GitHub release.

## Desktop Sessions

Desktop clients should construct `AepClient` in the trusted main process and inject `ProtectedRefreshTokenStore` with an operating-system protected storage adapter. Access and model tokens remain in memory; only the refresh token may be persisted through that adapter.

```ts
import {AepClient, ProtectedRefreshTokenStore} from '@aep/sdk-node';

const tokenStore = new ProtectedRefreshTokenStore(protectedStorage);
const client = new AepClient({
  baseUrl,
  agentVersion,
  platform: 'windows',
  tokenStore,
});

const state = await client.getSessionState();
if (state.status === 'recoverable') await client.restoreSession();
```

See the repository's desktop session integration guide for the Electron `safeStorage` adapter and renderer IPC boundary.

## Asynchronous route publication

The Control Service queue/query stage requires explicit
`AEP_GATEWAY_PUBLICATIONS_ENABLED=true` (default disabled). A separate in-cluster
`AEP_GATEWAY_PUBLICATION_RELAY_ENABLED` opt-in projects AEP CRs; without it tasks
stay queued. Operator observations are not read yet: conditions stay Unknown
even for projected tasks. Use only for isolated acceptance until the full path is ready. Existing
`publishDataPlaneRoutes()` retains its synchronous `200` contract. The SDK
does not fall back to it or automatically poll for readiness.

```ts
// Save this key before sending; reuse it and the same body for retries.
const publication = await client.createDataPlanePublication(publicationKey, {});
const current = await client.getDataPlanePublication(publication.operationId);
// projected is CR delivery only. Ready may still be Unknown.
const ready = current.conditions.find(condition => condition.type === 'Ready');
```

Both methods require a management session with `data_plane.write`. POST uses
bounded retries with the same validated `Idempotency-Key` and body; never
generate a fresh key for each retry or reuse one after `idempotencyExpiresAt`.
Persist the returned operation ID and query it after an uncertain outcome
when known. A `409 IDEMPOTENCY_KEY_CONFLICT` is an `AepProblem`, not a retry
signal. See [API semantics](../../docs/api-v1.md#asynchronous-operator-publication-queue-stage).

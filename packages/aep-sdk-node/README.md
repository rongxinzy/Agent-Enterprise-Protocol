# AEP Node SDK

[简体中文](README.zh-CN.md)

`@aep/sdk-node` is the Node.js communication and session SDK for Agent Enterprise Protocol v1. It supports Node.js 24 and publishes ESM, CommonJS, and TypeScript declarations.

The SDK owns protocol headers, RFC 9457 errors, bounded retries, token refresh rotation, and AEP API calls. Agent inbox/outbox persistence, Skill installation, model inference, and application UI remain the responsibility of the product client.

## Install

Install a fixed release version. Do not depend on a moving Git branch or a local workspace path in a product build.

```sh
npm install @aep/sdk-node@0.2.0
```

Until the package is published to the public npm registry, use the tarball and SHA-256 file attached to the matching `sdk-node-v0.2.0` GitHub release.

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

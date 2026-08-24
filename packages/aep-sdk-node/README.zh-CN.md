# AEP Node SDK

[English](README.md)

`@aep/sdk-node` 是 Agent Enterprise Protocol v1 的 Node.js 通信与会话 SDK，支持 Node.js 24，并同时发布 ESM、CommonJS 和 TypeScript 类型声明。

SDK 负责协议请求头、RFC 9457 错误、有界重试、Token 刷新轮换及 AEP API 调用。Agent inbox/outbox 持久化、Skill 安装、模型推理和产品 UI 仍由产品客户端负责。

## 安装

产品构建必须安装固定发布版本，不能依赖浮动 Git 分支或本地 workspace 路径。

```sh
npm install @aep/sdk-node@0.2.0
```

在包发布到公共 npm registry 前，请使用对应 `sdk-node-v0.2.0` GitHub Release 附带的 tarball 和 SHA-256 文件。

## 桌面会话

桌面客户端应在可信主进程中创建 `AepClient`，并向 `ProtectedRefreshTokenStore` 注入操作系统保护存储适配器。access token 和 model token 只保留在内存中，只有 refresh token 可以通过该适配器持久化。

```ts
import {AepClient, ProtectedRefreshTokenStore} from '@aep/sdk-node';

const tokenStore = new ProtectedRefreshTokenStore(protectedStorage);
const client = new AepClient({
  baseUrl,
  agentId,
  agentVersion,
  platform: 'windows',
  tokenStore,
});

const state = await client.getSessionState();
if (state.status === 'recoverable') await client.restoreSession();
```

Electron `safeStorage` 适配器和 Renderer IPC 边界详见仓库中的桌面会话接入文档。

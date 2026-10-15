# AEP Node SDK

[English](README.md)

`@aep/sdk-node` 是 Agent Enterprise Protocol v1 的 Node.js 通信与会话 SDK，支持 Node.js 24，并同时发布 ESM、CommonJS 和 TypeScript 类型声明。

SDK 负责协议请求头、RFC 9457 错误、有界重试、Token 刷新轮换及 AEP API 调用。Agent inbox/outbox 持久化、Skill 安装、模型推理和产品 UI 仍由产品客户端负责。

## 安装

产品构建必须安装固定发布版本，不能依赖浮动 Git 分支或本地 workspace 路径。

```sh
npm install @aep/sdk-node@0.2.9
```

内网或离线安装时，请使用对应 `sdk-node-v0.2.9` GitHub Release 附带的 tarball 和 SHA-256 文件。

## 桌面会话

桌面客户端应在可信主进程中创建 `AepClient`，并向 `ProtectedRefreshTokenStore` 注入操作系统保护存储适配器。access token 和 model token 只保留在内存中，只有 refresh token 可以通过该适配器持久化。

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

Electron `safeStorage` 适配器和 Renderer IPC 边界详见仓库中的桌面会话接入文档。

## 异步路由发布

管控服务已实现入队／查询，以 `AEP_GATEWAY_PUBLICATIONS_ENABLED=true` 显式开启，
默认关闭。另以 `AEP_GATEWAY_PUBLICATION_RELAY_ENABLED` 开启集群内 CR 投递，否则保持
queued。operator 观测读取尚未实现，即使 projected，conditions 仍为 Unknown；
完整链路验收前仅用于隔离环境的入队验证。
现有 `publishDataPlaneRoutes()` 保留同步 `200` 契约。SDK 不自动回退至旧接口，
也不自动轮询直到 Ready。

```ts
// 发送前保存此键；重试必须复用原键与原请求体。
const publication = await client.createDataPlanePublication(publicationKey, {});
const current = await client.getDataPlanePublication(publication.operationId);
// projected 仅代表 CR 投递完成，Ready 仍可能为 Unknown。
const ready = current.conditions.find(condition => condition.type === 'Ready');
```

两者均需持有 `data_plane.write` 的管理会话。POST 在有界重试中保持同一个合法
`Idempotency-Key` 与请求体；不要为每次重试生成新键，也不要在
`idempotencyExpiresAt` 后复用旧键。保存返回的 operationId，响应不确定且已知
operationId 时查询原任务。`409 IDEMPOTENCY_KEY_CONFLICT` 通过 `AepProblem`
返回，不是重试信号。参见 [API 语义](../../docs/api-v1.zh-CN.md#operator-异步发布入队阶段)。

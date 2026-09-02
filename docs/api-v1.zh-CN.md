# AEP v1 API 指南

简体中文 | [English](api-v1.md)

本文说明 AEP v1 HTTP(S) REST API。机器可读契约分为
[核心 OpenAPI 文档](../openapi/aep-v1.openapi.yaml)、
[管控事件 OpenAPI 文档](../openapi/aep-v1-control-events.openapi.yaml)和
[认证 OpenAPI 文档](../openapi/aep-v1-authentication.openapi.yaml)。

## 1. 通用约定

HTTPS 部署示例：`https://enterprise.example.com/aep/v1`
HTTP 部署示例：`http://enterprise.example.com/aep/v1`
本地示例：`http://localhost:8080/aep/v1`

Agent 请求头：

```http
Authorization: Bearer <access-token>
X-AEP-Agent-ID: <stable-agent-instance-id>
X-AEP-Protocol-Version: 1.0
X-Request-ID: <request-id>
```

JSON 字段使用 `camelCase`，时间使用 RFC 3339 UTC，错误使用 RFC 9457
`application/problem+json`。AEP v1 使用 REST 轮询，不使用 SSE 或 WebSocket。

为支持客户端发现兼容性，metadata 可以不带协议版本头访问。其他所有 /aep/v1 请求必须发送 X-AEP-Protocol-Version: 1.0；缺失或不支持的值会返回 RFC 9457 的 426 状态及 X-AEP-Supported-Protocol-Versions。

## 2. 端点总览

### 服务与认证

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/metadata` | 查询 AEP 版本和服务能力 |
| GET | `/auth/methods` | 查询企业可用登录方式 |
| POST | `/auth/password/login` | 使用管理员创建的知远平台账号登录 |
| POST | `/auth/federated/start` | 发起甲方联合登录 |
| POST | `/auth/exchange` | 交换联合登录一次性授权码 |
| POST | `/auth/refresh` | 刷新会话 |
| POST | `/auth/logout` | 撤销当前 refresh 会话 |
| POST | `/agent/activation` | 将本地验签的 License 证据交换为 entitlement token |

### Agent API

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/agent/me` | 查询当前用户和企业身份 |
| GET | `/agent/skills/manifest` | 获取完整的期望 Skill 清单 |
| GET | `/agent/skills/{skillId}/versions/{version}/package` | 下载 Skill ZIP 包 |
| POST | `/agent/skills/sync-results` | 上报 Skill 同步结果 |
| POST | `/agent/events/batch` | 幂等批量上传事件 |
| POST | `/agent/heartbeat` | 上报存活状态并发现待处理管控事件 |
| GET | `/agent/control-events` | 获取当前 Agent 未确认的适用管控事件 |
| POST | `/agent/control-events/{deliveryId}/acknowledge` | 确认事件已持久化接收 |
| POST | `/agent/control-events/{deliveryId}/result` | 上报 Task 执行状态 |
| GET | `/agent/credentials` | 查询已授权的凭证元数据 |
| POST | `/agent/credentials/{credentialId}/resolve` | 获取可下发到 Agent 的凭证 |
| GET | `/agent/models` | 查询当前用户可见模型 |

## 3. 元数据与认证

### `GET /metadata`

```json
{
  "service": "zhiyuan-enterprise",
  "protocol": "aep",
  "minimumVersion": "1.0",
  "maximumVersion": "1.0",
  "features": ["skill-management", "event-upload", "credential-delivery", "model-catalog"]
}
```

### `GET /auth/methods`

示例：`GET /auth/methods?enterpriseHint=example`

```json
{
  "enterprise": {"id": "enterprise_001", "name": "示例企业"},
  "preferredMethodId": "enterprise-sso",
  "methods": [
    {"id": "enterprise-sso", "type": "federated", "protocol": "oidc", "displayName": "企业统一登录"},
    {"id": "zhiyuan-password", "type": "password", "displayName": "知远账号"}
  ]
}
```

### `POST /auth/password/login`

```json
{
  "enterpriseId": "enterprise_001",
  "username": "liming",
  "password": "user-entered-password",
  "agentId": "0198a910-5235-7b24-9b63-4b7dd46782e0",
  "agentVersion": "1.8.0",
  "platform": "windows"
}
```

账号由管理员手动创建或批量导入，不代表开放自助注册。任何部署阶段的密码登录都可以使用
HTTP 或 HTTPS。明文 HTTP 会暴露传输中的账号密码和 bearer token，因此在可信内网之外
强烈建议使用 HTTPS。

### `POST /auth/federated/start`

```json
{
  "enterpriseId": "enterprise_001",
  "methodId": "enterprise-sso",
  "redirectUri": "zhiyuan://auth/callback",
  "codeChallenge": "base64url-sha256-challenge"
}
```

```json
{
  "transactionId": "login_tx_123",
  "authorizationUrl": "https://idp.example.com/authorize?...",
  "state": "opaque-state",
  "expiresIn": 300
}
```

Agent 在系统浏览器打开 `authorizationUrl`，回调时校验 `state`。甲方账号密码不经过 Agent。

### `POST /auth/exchange`

```json
{
  "transactionId": "login_tx_123",
  "authorizationCode": "one-time-code",
  "redirectUri": "zhiyuan://auth/callback",
  "codeVerifier": "pkce-verifier",
  "agentId": "0198a910-5235-7b24-9b63-4b7dd46782e0",
  "agentVersion": "1.8.0",
  "platform": "windows"
}
```

```json
{
  "accessToken": "eyJ...",
  "refreshToken": "refresh-token",
  "modelAccessToken": "eyJ-model...",
  "tokenType": "Bearer",
  "expiresIn": 7200,
  "modelAccessExpiresIn": 7200,
  "passwordChangeRequired": false
}
```

密码登录和联合登录交换返回相同的会话结构。授权码只能使用一次。model access token 在
有效期内可直接用于模型网关。

`passwordChangeRequired` 为 true 时，会话只能读取当前身份、修改密码或退出，model token
不包含任何模型作用域；其他操作返回 `PASSWORD_CHANGE_REQUIRED`。密码登录失败使用共享的
渐进退避，退避期间返回带 `Retry-After` 的 `429`。

### `POST /auth/refresh`

请求：

```json
{"refreshToken": "refresh-token", "agentId": "0198a910-5235-7b24-9b63-4b7dd46782e0"}
```

响应使用上述 token 结构。响应中出现新的 refresh token 时，客户端必须替换旧值。

### `POST /auth/logout`

请求：`{"refreshToken":"refresh-token"}`。成功返回 `204 No Content`。

### `POST /agent/activation`

Agent 在本地验签企业 License 后，将 License 证据交换为短期服务端签发的
entitlement token：

```json
{
  "licenseId": "lic_123",
  "licenseDigest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "deploymentId": "deployment_001",
  "expiresAt": "2027-01-01T00:00:00Z",
  "features": ["enterprise.models", "enterprise.skills"]
}
```

响应包含 `entitlementToken`、`expiresAt` 和规范化后的功能列表。Token 绑定当前
认证企业、用户和 Agent。Control Service 不签发 License，且绝不能接收 License 私钥。

## 4. 当前身份

### `GET /agent/me`

```json
{
  "user": {"id": "user_123", "displayName": "李明", "email": "liming@example.com"},
  "enterprise": {"id": "enterprise_001", "name": "示例企业"},
  "roles": ["employee"],
  "sessionExpiresAt": "2026-08-19T10:00:00Z",
  "passwordChangeRequired": false
}
```

## 5. Skill 同步

### `GET /agent/skills/manifest`

客户端应在 `If-None-Match` 中发送上次的 `ETag`。内容未变化时，服务端返回
`304 Not Modified`。

```json
{
  "revision": "43",
  "generatedAt": "2026-08-19T08:00:00Z",
  "skills": [{
    "id": "docx",
    "name": "Word Documents",
    "description": "Create and edit Word documents.",
    "version": "1.3.0",
    "enabled": true,
    "package": {
      "url": "/aep/v1/agent/skills/docx/versions/1.3.0/package",
      "sha256": "8be72b3f1f47e36014fc8e1af54b250098201b1e2ea9a260153d69f7e64f1930",
      "size": 18342
    }
  }]
}
```

该响应是完整清单，不在清单中的托管 Skill 应被删除。

### `GET /agent/skills/{skillId}/versions/{version}/package`

返回 `application/zip`。服务端再次检查授权，客户端在解压前校验清单中的 SHA-256。

### `POST /agent/skills/sync-results`

```json
{
  "manifestRevision": "43",
  "startedAt": "2026-08-19T08:01:00Z",
  "completedAt": "2026-08-19T08:01:03Z",
  "status": "partial",
  "items": [
    {"skillId": "docx", "version": "1.3.0", "action": "update", "status": "success"},
    {
      "skillId": "xlsx",
      "version": "2.0.0",
      "action": "install",
      "status": "failed",
      "errorCode": "PACKAGE_HASH_MISMATCH",
      "message": "Downloaded package digest did not match the manifest."
    }
  ]
}
```

## 6. 遥测事件上传

### `POST /agent/events/batch`

```json
{
  "events": [{
    "eventId": "0198a91b-d0d4-70a1-b38e-c43482f6798d",
    "type": "skill.updated",
    "occurredAt": "2026-08-19T08:01:03Z",
    "resource": {"type": "skill", "id": "docx"},
    "result": "success",
    "metadata": {"fromVersion": "1.2.0", "toVersion": "1.3.0"}
  }]
}
```

```json
{"accepted": ["0198a91b-d0d4-70a1-b38e-c43482f6798d"], "rejected": []}
```

被拒绝的项目包含 `eventId`、`code` 和 `message`。重复事件 ID 按已接受处理，客户端
可以安全重试。

## 7. 管控事件

### `POST /agent/heartbeat`

心跳用于上报存活状态，响应只返回管控事件发现信息。

```json
{
  "agentVersion": "1.8.0",
  "platform": "windows",
  "lastControlEventCursor": "142",
  "status": "online"
}
```

```json
{
  "serverTime": "2026-08-19T08:00:00Z",
  "controlEvents": {
    "pending": true,
    "watermark": "147"
  },
  "nextHeartbeatAfterSeconds": 30
}
```

pending 标志只用于查询优化，不是可靠性边界。Agent 仍需定期查询管控事件，避免错误或
过期标志导致事件永久不可见。

### `GET /agent/control-events`

查询参数为 `afterCursor` 和 `limit`。服务端根据已认证会话计算适用的全局、组织、用户和
Agent 作用域。

```json
{
  "items": [{
    "deliveryId": "delivery_001",
    "eventId": "event_001",
    "cursor": "143",
    "type": "skill.manifest.changed",
    "scope": {"type": "organization", "id": "org_001"},
    "resource": {"type": "skill", "id": "docx", "revision": "18"},
    "task": {"type": "skill.reconcile"},
    "createdAt": "2026-08-19T07:59:00Z",
    "expiresAt": "2026-08-20T07:59:00Z"
  }],
  "nextCursor": "143",
  "watermark": "147"
}
```

读取响应不代表消费。在 Agent 确认接收前，服务端可以再次返回该投递。Agent 使用
`deliveryId` 和 `eventId` 去重。

### `POST /agent/control-events/{deliveryId}/acknowledge`

Agent 只有在事件已经提交到本地持久化收件箱后才能调用该接口。该操作必须幂等。

```json
{
  "status": "received",
  "receivedAt": "2026-08-19T08:00:01Z"
}
```

成功返回 `204 No Content`。接收确认会停止网络重复投递，但不代表 Task 执行成功。

### `POST /agent/control-events/{deliveryId}/result`

Agent 上报 `running`、`succeeded` 或 `failed`。重复提交相同状态和结果必须幂等。

```json
{
  "status": "succeeded",
  "completedAt": "2026-08-19T08:00:03Z",
  "appliedRevision": "18"
}
```

失败示例：

```json
{
  "status": "failed",
  "completedAt": "2026-08-19T08:00:03Z",
  "errorCode": "SKILL_RECONCILE_FAILED",
  "message": "The Skill package could not be installed.",
  "retryable": true
}
```

服务端分别维护接收状态和执行状态。即使源事件的作用域是全局、组织或用户，每个适用
Agent 也必须拥有独立的投递记录。

## 8. 凭证

### `GET /agent/credentials`

```json
{
  "credentials": [{
    "id": "credential_123",
    "name": "Internal Search API",
    "service": "internal-search",
    "deliveryMode": "agent",
    "maskedValue": "sk-****9f2a",
    "updatedAt": "2026-08-19T07:00:00Z"
  }]
}
```

### `POST /agent/credentials/{credentialId}/resolve`

请求：`{"purpose":"Connect to the internal search service"}`。

```json
{
  "credentialId": "credential_123",
  "type": "api_key",
  "value": "sk-live-value",
  "expiresAt": null
}
```

响应包含 `Cache-Control: no-store`。获取 `server_only` 凭证时返回
`CREDENTIAL_NOT_DELIVERABLE`。

## 9. 模型

### `GET /agent/models`

```json
{
  "models": [
    {
      "id": "enterprise-qwen-32b",
      "displayName": "Enterprise Qwen 32B",
      "sourceType": "enterprise_open_source",
      "protocol": "openai-compatible",
      "endpoint": "https://models.example.com/v1",
      "upstreamModel": "qwen3-32b",
      "capabilities": ["text", "tools", "streaming"],
      "contextWindow": 131072,
      "isDefault": true,
      "enabled": true
    }
  ]
}
```

对于远程模型，Agent 使用登录或刷新时获得的 model access token 访问模型描述中声明的
网关地址。网关逐次在本地校验 token，不同步调用管控服务重新授权。AEP 不重新定义推理
请求和响应格式。

## 10. 管理端 API

管理端端点必须使用管理员身份。

### 平台账号

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/users` | 查询或手动创建知远平台账号 |
| POST | `/admin/users/import` | 批量导入知远平台账号 |
| PATCH | `/admin/users/{userId}` | 启用、禁用或更新账号 |
| POST | `/admin/users/{userId}/reset-password` | 设置新的临时密码 |

### Skill

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/skills` | 查询或创建 Skill 元数据 |
| GET, PATCH, DELETE | `/admin/skills/{skillId}` | 读取、更新或撤回 Skill |
| POST | `/admin/skills/{skillId}/versions` | 使用 multipart 上传 ZIP |
| POST | `/admin/skills/{skillId}/versions/{version}/publish` | 发布版本 |
| DELETE | `/admin/skills/{skillId}/versions/{version}` | 撤回版本 |
| GET, POST | `/admin/skill-assignments` | 查询或创建授权关系 |
| DELETE | `/admin/skill-assignments/{assignmentId}` | 删除授权关系 |

授权示例：

```json
{"skillId": "docx", "subject": {"type": "role", "id": "employee"}}
```

### 凭证

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/credentials` | 查询掩码元数据或创建凭证 |
| GET, PATCH, DELETE | `/admin/credentials/{credentialId}` | 读取、更新或撤销凭证 |
| POST | `/admin/credentials/{credentialId}/rotate` | 替换凭证明文 |
| GET, POST | `/admin/credential-assignments` | 查询或创建授权关系 |
| DELETE | `/admin/credential-assignments/{assignmentId}` | 删除授权关系 |

创建示例：

```json
{
  "name": "Internal Search API",
  "service": "internal-search",
  "type": "api_key",
  "deliveryMode": "agent",
  "value": "sk-live-value",
  "enabled": true
}
```

读取响应永不返回 `value`，轮换接口接收新的 `value`。

### 模型

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/models` | 查询或创建模型描述 |
| GET, PATCH, DELETE | `/admin/models/{modelId}` | 读取、更新或删除模型 |
| GET, POST | `/admin/model-assignments` | 查询或创建授权关系 |
| DELETE | `/admin/model-assignments/{assignmentId}` | 删除授权关系 |

管理端使用的 `credentialId` 不得出现在 Agent 模型目录中。

### 管控事件

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/control-events` | 查询或发布管控事件 |
| GET | `/admin/control-events/{eventId}` | 查询事件及聚合状态 |
| POST | `/admin/control-events/{eventId}/cancel` | 取消尚未接收的投递 |
| GET | `/admin/control-events/{eventId}/deliveries` | 查询每个 Agent 的投递状态 |

发布示例：

```json
{
  "type": "skill.manifest.changed",
  "scope": {"type": "organization", "id": "org_001", "includeDescendants": true},
  "resource": {"type": "skill", "id": "docx", "revision": "18"},
  "task": {"type": "skill.reconcile"},
  "expiresAt": "2026-08-20T07:59:00Z",
  "supersedesKey": "skill:docx:org_001"
}
```

服务端负责解析适用 Agent。取消操作只影响尚未进入 `received` 的投递，不能撤销 Agent
已经接收并执行的工作。
发布具有相同 `supersedesKey` 的新事件时，服务端将旧事件中尚未接收的投递标记为
`superseded`。

### 遥测事件

`GET /admin/events` 支持 `cursor`、`limit`、`userId`、`agentId`、`type`、
`resourceType`、`resourceId`、`result`、`occurredAfter` 和 `occurredBefore`。

## 11. 错误与重试

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

通用错误码包括 `INVALID_REQUEST`、`TOKEN_INVALID`、`ACCESS_DENIED`、
`SKILL_NOT_ASSIGNED`、`CREDENTIAL_NOT_DELIVERABLE`、`MODEL_NOT_ALLOWED`、
`RESOURCE_NOT_FOUND`、`VERSION_CONFLICT`、`RATE_LIMITED` 和 `INTERNAL_ERROR`。

安全的读取请求采用指数退避重试。事件批次依据 `eventId` 幂等。授权码交换和凭证获取不得
盲目重试。收到 `429` 或 `503` 时遵守 `Retry-After`。

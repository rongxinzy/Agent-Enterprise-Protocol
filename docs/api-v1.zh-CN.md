# AEP v1 API 指南

简体中文 | [English](api-v1.md)

协议档位：`1.0.0-rc.1`

本文说明 AEP v1 HTTP(S) REST API。机器可读契约分为
[核心 OpenAPI 文档](../openapi/aep-v1.openapi.yaml)、
[管控事件 OpenAPI 文档](../openapi/aep-v1-control-events.openapi.yaml)和
[认证 OpenAPI 文档](../openapi/aep-v1-authentication.openapi.yaml)。

## 1. 通用约定

HTTPS 部署示例：`https://enterprise.example.com/aep/v1`
HTTP 部署示例：`http://enterprise.example.com/aep/v1`
本地示例：`http://localhost:8080/aep/v1`

认证请求头：

```http
Authorization: Bearer <access-token>
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
| GET | `/.well-known/jwks.json` | 获取用于校验 AEP 与模型 token 的公钥 |
| GET | `/metadata` | 查询 AEP 版本和服务能力 |
| GET | `/auth/methods` | 查询部署可用登录方式 |
| POST | `/auth/password/login` | 使用管理员创建的知远平台账号登录 |
| POST | `/auth/password/change` | 修改当前账号密码 |
| POST | `/auth/federated/start` | 发起甲方联合登录 |
| POST | `/auth/exchange` | 交换联合登录一次性授权码 |
| POST | `/auth/refresh` | 刷新会话 |
| POST | `/auth/logout` | 撤销当前 refresh 会话 |
| POST | `/user/activation` | 使用已认证部署会话换取 entitlement token |

联合认证端点只为已配置的身份适配器保留。内置 mock 仅用于开发，纯密码生产档位不会公布
该能力。

### 用户运行时 API

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/user/me` | 查询当前用户、部署、会话、角色和权限 |
| GET | `/user/skills/manifest` | 获取完整的期望 Skill 清单 |
| GET | `/user/skills/{skillId}/versions/{version}/package` | 下载 Skill ZIP 包 |
| POST | `/user/skills/sync-results` | 上报 Skill 同步结果 |
| POST | `/user/events/batch` | 幂等批量上传事件 |
| POST | `/user/heartbeat` | 上报会话存活状态并发现待处理管控事件 |
| GET | `/user/control-events` | 获取当前会话未确认的适用管控事件 |
| POST | `/user/control-events/{deliveryId}/acknowledge` | 确认事件已持久化接收 |
| POST | `/user/control-events/{deliveryId}/result` | 上报 Task 执行状态 |
| GET | `/user/credentials` | 查询已授权的凭证元数据 |
| POST | `/user/credentials/{credentialId}/resolve` | 获取可下发到客户端的凭证 |
| GET | `/user/models` | 查询当前用户可见模型 |

## 3. 元数据与认证

### `GET /metadata`

```json
{
  "service": "aep-control-service",
  "supportedProtocolVersions": ["1.0"],
  "capabilities": ["password_auth", "skills", "telemetry", "control_events", "model_gateway", "credentials"],
  "jwksUri": "/.well-known/jwks.json",
  "deploymentId": "deployment_001",
  "deployment": {"id": "deployment_001", "name": "示例部署"},
  "modelGateway": {"baseUrl": "https://gateway.example.com/v1", "protocol": "openai-compatible", "apiVersion": "v1"}
}
```

### `GET /auth/methods`

示例：`GET /auth/methods?deploymentHint=deployment_001`

```json
{
  "deployment": {"id": "deployment_001", "name": "示例部署"},
  "deploymentId": "deployment_001",
  "preferredMethodId": "zhiyuan-password",
  "methods": [
    {"id": "zhiyuan-password", "type": "password", "displayName": "知远账号"}
  ]
}
```

### `POST /auth/password/login`

```json
{
  "deploymentId": "deployment_001",
  "sessionId": "terminal_01",
  "username": "liming",
  "password": "user-entered-password"
}
```

账号由管理员手动创建或批量导入，不代表开放自助注册。任何部署阶段的密码登录都可以使用
HTTP 或 HTTPS。明文 HTTP 会暴露传输中的账号密码和 bearer token，因此在可信内网之外
强烈建议使用 HTTPS。

### `POST /auth/password/change`

请求：`{"currentPassword":"old-password","newPassword":"new-long-password"}`。服务端修改当前
账号密码、撤销该账号的其他 refresh 会话，并返回
`passwordChangeRequired` 为 false 的新 token 结构。

### `POST /auth/federated/start`

```json
{
  "deploymentId": "deployment_001",
  "sessionId": "terminal_01",
  "methodId": "customer-sso",
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
  "codeVerifier": "pkce-verifier"
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
  "deploymentId": "deployment_001",
  "sessionId": "terminal_01",
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
{"refreshToken": "refresh-token", "sessionId": "terminal_01"}
```

响应使用上述 token 结构。响应中出现新的 refresh token 时，客户端必须替换旧值。

### `POST /auth/logout`

请求：`{"refreshToken":"refresh-token"}`。成功返回 `204 No Content`。

### `POST /user/activation`

客户端提交空的激活请求。Control Service 使用部署端已验签并注册的 License，
交换为短期服务端签发的 entitlement token：

```json
{}
```

响应包含 `entitlementToken`、`expiresAt` 和规范化后的功能列表。Token 绑定当前
认证部署、用户和会话；生产模型网关会在状态缓存窗口内复核会话和请求模型的
当前授权。Control Service 不签发 License，且绝不能接收 License 私钥。

## 4. 当前身份

### `GET /user/me`

```json
{
  "user": {"id": "user_123", "displayName": "李明", "email": "liming@example.com"},
  "deployment": {"id": "deployment_001", "name": "示例部署"},
  "deploymentId": "deployment_001",
  "sessionId": "terminal_01",
  "roles": ["employee"],
  "permissions": ["models.read", "skills.read"],
  "sessionExpiresAt": "2026-08-19T10:00:00Z",
  "passwordChangeRequired": false
}
```

## 5. Skill 同步

### `GET /user/skills/manifest`

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
      "url": "/aep/v1/user/skills/docx/versions/1.3.0/package",
      "sha256": "8be72b3f1f47e36014fc8e1af54b250098201b1e2ea9a260153d69f7e64f1930",
      "size": 18342
    }
  }]
}
```

该响应是完整清单，不在清单中的托管 Skill 应被删除。

### `GET /user/skills/{skillId}/versions/{version}/package`

返回 `application/zip`。服务端再次检查授权，客户端在解压前校验清单中的 SHA-256。

### `POST /user/skills/sync-results`

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

### `POST /user/events/batch`

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

### `POST /user/heartbeat`

心跳用于上报存活状态，响应只返回管控事件发现信息。

```json
{
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

### `GET /user/control-events`

查询参数为 `afterCursor` 和 `limit`。服务端根据已认证用户及其角色、团队绑定计算适用的
`global`、`team`、`role` 和 `user` 作用域。

```json
{
  "items": [{
    "deliveryId": "delivery_001",
    "eventId": "event_001",
    "cursor": "143",
    "type": "skill.manifest.changed",
    "scope": {"type": "team", "id": "team_001"},
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

### `POST /user/control-events/{deliveryId}/acknowledge`

Agent 只有在事件已经提交到本地持久化收件箱后才能调用该接口。该操作必须幂等。

```json
{
  "status": "received",
  "receivedAt": "2026-08-19T08:00:01Z"
}
```

成功返回 `204 No Content`。接收确认会停止网络重复投递，但不代表 Task 执行成功。

### `POST /user/control-events/{deliveryId}/result`

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

服务端分别维护接收状态和执行状态。即使源事件的作用域是全局、团队、角色或用户，每个
适用用户会话也拥有独立的投递记录。用户离线时，active 且未过期的事件仍会保留；后续建立
匹配会话时，服务端为该会话创建 delivery。

## 8. 凭证

### `GET /user/credentials`

```json
{
  "credentials": [{
    "id": "credential_123",
    "name": "Internal Search API",
    "service": "internal-search",
    "type": "api_key",
    "deliveryMode": "client",
    "maskedValue": "sk-****9f2a",
    "enabled": true,
    "updatedAt": "2026-08-19T07:00:00Z"
  }],
  "nextCursor": null
}
```

### `POST /user/credentials/{credentialId}/resolve`

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

### `GET /user/models`

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

每个用户在创建或导入时必须至少绑定一个角色和一个团队。

### RBAC 与会话

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/admin/permissions` | 读取稳定的权限目录 |
| GET, POST | `/admin/roles` | 查询或创建部署角色 |
| GET, PATCH, DELETE | `/admin/roles/{roleId}` | 读取、更新或删除非系统角色 |
| GET, POST | `/admin/teams` | 查询或创建部署团队 |
| GET, PATCH, DELETE | `/admin/teams/{teamId}` | 读取、更新或删除团队 |
| PUT | `/admin/users/{userId}/rbac` | 替换用户的角色和团队绑定 |
| GET | `/admin/sessions` | 查询用户会话及心跳状态 |
| POST | `/admin/sessions/{sessionId}/revoke` | 撤销一个用户会话 |

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

Skill ID 是最长 64 个字符的可移植标识符，版本标识符最长 128 个字符。两者都必须
以字母或数字开头和结尾；Skill ID 内部可使用 `.`、`_`、`-`，版本还可使用 `+`。
接口不接受路径分隔符或路径逃逸片段。

授权示例：

```json
{"skillId": "docx", "subject": {"type": "role", "id": "employee"}}
```

Skill、凭证和模型授权均支持 `user`、`role` 或 `team` 主体。用户的最终访问权限取其直接
授权、角色授权和团队授权的并集。

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
  "deliveryMode": "client",
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

### License

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/admin/licenses` | 查询已注册部署 License 元数据 |
| GET | `/admin/licenses/{licenseId}` | 读取 License 元数据和激活状态 |
| POST | `/admin/licenses/import` | 注册供应商签发的离线 License |
| POST | `/admin/licenses/{licenseId}/revoke` | 撤销已注册 License |

管控服务使用配置的公钥材料验证供应商签名。License 私钥和签名器代码位于本仓库之外，
不得发送给服务端。

### 数据平面

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, PUT | `/admin/data-plane/desired-state` | 读取或发布模型网关期望状态 |
| GET | `/admin/data-plane/status` | 读取最近一次调和观测状态 |

期望状态只引用外部 Secret 名称和键，不包含模型服务商凭证明文。

### 管控事件

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/control-events` | 查询或发布管控事件 |
| GET | `/admin/control-events/{eventId}` | 查询事件及聚合状态 |
| POST | `/admin/control-events/{eventId}/cancel` | 取消尚未接收的投递 |
| GET | `/admin/control-events/{eventId}/deliveries` | 查询每个会话的投递状态 |

发布示例：

```json
{
  "type": "skill.manifest.changed",
  "scope": {"type": "team", "id": "team_001"},
  "resource": {"type": "skill", "id": "docx", "revision": "18"},
  "task": {"type": "skill.reconcile"},
  "expiresAt": "2026-08-20T07:59:00Z",
  "supersedesKey": "skill:docx:team_001"
}
```

服务端负责解析适用用户会话。取消操作只影响尚未进入 `received` 的投递，不能撤销客户端
已经接收并执行的工作。
发布具有相同 `supersedesKey` 的新事件时，服务端将旧事件中尚未接收的投递标记为
`superseded`。

### 遥测事件

`GET /admin/events` 支持 `cursor`、`limit`、`userId`、`sessionId`、`type`、
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

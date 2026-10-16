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
  "password": "user-entered-password",
  "client": {"name": "zhiyuan-desktop", "version": "1.4.0", "deviceId": "device_42"}
}
```

账号由管理员手动创建或批量导入，不代表开放自助注册。`password` 的前后空白字符在
校验前会被去除。任何部署阶段的密码登录都可以使用
HTTP 或 HTTPS。明文 HTTP 会暴露传输中的账号密码和 bearer token，因此在可信内网之外
强烈建议使用 HTTPS。

`client` 为可选字段。提供时会记录到签发的会话上，并通过 `GET /admin/sessions` 暴露，
便于管理员在撤销会话前区分不同终端。`name` 必填（1-64 字符）；`version`（最多 64 字符）
和 `deviceId`（最多 128 字符）可选。未提供该字段时，服务端从 User-Agent
推导一个粗粒度标签（`browser`、`electron`、`node` 或 `curl`），无法识别则不记录。
这些值是客户端自报的管理提示，不是经过验证的声明。

### `POST /auth/password/change`

请求：`{"newPassword":"new-long-password"}`。会话 bearer 令牌即完成身份认证。
`currentPassword` 是可选的加强校验：提供时先去除前后空白再校验，不匹配返回
`401 INVALID_CREDENTIALS`；省略时仅凭会话授权改密。`newPassword` 不允许
前后空白字符（`PASSWORD_POLICY_VIOLATION`）。服务端修改当前
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

返回 `application/zip`。服务端再次检查与清单一致的生效条件——授权、授权到期、Skill 启用与版本发布——因此清单中不存在的 Skill 也无法下载。客户端在解压前校验清单中的 SHA-256。

### `POST /user/skills/sync-results`

```json
{
  "revision": "43",
  "status": "completed",
  "items": [
    {"skillId": "docx", "status": "updated"},
    {"skillId": "xlsx", "status": "installed"}
  ]
}
```

服务端读取报告的 `revision`、`status` 与 `items`，以及每个条目的 `skillId` 与 `status`
（`installed`、`updated`、`unchanged` 视为已安装；`removed` 及其它取值视为未安装）。
报告级 `startedAt`、`completedAt`，以及条目级 `version`、`action`、`errorCode`、`message`
会被接受并保留在存储的 payload 中，但不影响服务端行为。上文未列出的字段会被 `400` 拒绝；
上报里若写的是 `manifestRevision` 而不是 `revision`，同样会被 `400` 拒绝。

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

可选的 `client` 对象（形状与密码登录一致）会替换会话上记录的客户端标识，客户端升级
版本号后无需重新登录即可在 `GET /admin/sessions` 中体现。与登录不同，心跳不做
User-Agent 回退；未提供该字段时会话保留已记录的标识。

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

`watermark` 是该会话已投递的最大 `cursor`，从未投递时为空串 `""`；取值与心跳
`controlEvents.watermark` 一致。它只用于报告进度：翻页请用 `nextCursor`
（`afterCursor` 传上一页最后一条投递的 `cursor`），因为水印可能超前于仍在
pending 的投递。

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

每个用户在创建或导入时必须至少绑定一个角色和一个团队。密码长度须为 6 到 1024 个字符。临时密码的前后空白：创建或重置时拒绝（`PASSWORD_POLICY_VIOLATION`）；导入时则去除（trim），因为导入行来自导出文件和电子表格。整批行都被拒绝的导入是失败而不是空成功：服务端返回 `422 USER_IMPORT_REJECTED`，并把逐行原因附在 problem 文档里，因为一个账号都没创建的批次并没有生效；只要创建了至少一个账号，仍是 `200` 并返回 `created`/`rejected` 计数。创建或导入未提供临时密码（或导入 trim 后为空）时，服务端分配默认密码 `123456`。每个账号都带有 `kind` 标注（`human` 或 `agent`）；平台用户列表只返回人类账号，数字员工（`kind=agent`）由数字员工目录列出。当前身份接口对已认证主体使用同样的标注。对调用者本人重置密码且携带 `requirePasswordChange=true` 时，服务端返回 `409 SELF_PASSWORD_RESET_RESTRICTED`；请改用自助改密接口或传 `requirePasswordChange=false`。

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

`GET /admin/sessions` 的每一项带有可选的 `client` 对象
（如 `{"name": "zhiyuan-desktop", "version": "1.4.0", "deviceId": "device_42"}`），
未知时为 `null`。它用于区分同一账号的不同终端（例如 Admin Console 浏览器登录与企业
桌面客户端），避免管理员撤销错误的会话行。

团队构成一个森林。`POST /admin/teams` 接受可选的 `parentId`，每个团队都带有创建时即固定的
`path` 和 `depth`；父团队创建后不可变更，因此不可能出现环形层级。删除仍被引用的团队时，
存在子团队返回 `TEAM_HAS_CHILDREN`（409），是数字员工主团队返回 `TEAM_HAS_AGENTS`（409），
其他**外键**引用返回 `TEAM_IN_USE`（409）。通过多态 subject 列指向主体的授权（技能/模型/凭据授权与数据范围规则）**不带外键**：删除角色或团队会在同一事务内把它们一并删除，而不是拦下删除 —— 因此之后复用同一标识符的主体不会继承它们。

### 数字员工

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/agents` | 查询或创建数字员工账号 |
| DELETE | `/admin/agents/{agentId}` | 删除数字员工账号 |
| PUT | `/admin/agents/{agentId}/profile` | 更新单个数字员工的档案 |

读取需要 `users.read` 权限；写入接受 `agents.write`（可下放的数字员工生命周期权限，
不能触碰人类账号）或 `users.write`，完整管理员始终满足。部署可通过
`AEP_MAX_RESIDENT_AGENTS` 限制常驻（非临时）数字员工数量，超出返回
`AGENT_QUOTA_EXCEEDED`（409）。数字员工是
带主团队、展示头衔、可选描述和可选提示 Skill 的平台账号。主团队总会与 `teamIds` 一并授权，
且调用方必须有权授予请求的每个角色和团队。

`password` 至少 8 个字符，仅以 Argon2id 哈希存储。`GET /admin/agents` 使用 `cursor` 和
`limit`（1-200，默认 50）分页，返回 `agents` 和最后一页为 `null` 的 `nextCursor`。

档案更新接受 `displayTitle`、`description`、`homeTeamId` 和 `promptSkillId`；每个字段都可
传 null 清除已存储的值。

临时会话级实例：`ephemeral: true` 必须携带未来的 `expiresAt`；过期后登录与刷新以
`AGENT_EXPIRED`（403）拒绝。`scopeFromUserId` 把所指用户当前的数据范围快照冻结到该账号
（可见团队子树生成 management_scope 规则，显式拒绝原样复制），且请求的团队必须已落在该
范围之内；来源账号必须处于启用状态。`modelIds` 随账号一并创建模型授权。目录默认隐藏临时实例，传
`includeEphemeral=true` 才会列出。`DELETE /admin/agents/{agentId}` 连同绑定、授权与规则
一并删除账号；存在未撤销会话时以 `AGENT_HAS_SESSIONS`（409）拒绝，需先撤销会话。

错误码：

| 错误码 | 状态 | 含义 |
| --- | --- | --- |
| `INVALID_AGENT` | 400 | 用户名、显示名、密码、主团队、提示 Skill 无效，或包含未知的角色、团队 |
| `USER_RBAC_REQUIRED` | 400 | 至少需要一个角色和一个团队（含主团队） |
| `INVALID_ROLE`、`INVALID_TEAM` | 400 | 角色或团队绑定无效 |
| `ROLE_GRANT_FORBIDDEN`、`TEAM_GRANT_FORBIDDEN` | 403 | 调用方无权授予请求的角色或团队 |
| `AGENT_EXISTS` | 409 | 用户名已存在 |
| `INVALID_SCOPE_SOURCE` | 400 | 范围来源用户不存在或已停用，或请求的团队超出来源用户的可见范围 |
| `UNKNOWN_MODEL` | 400 | 请求的模型授权引用了不存在的模型 |
| `AGENT_HAS_SESSIONS` | 409 | 存在活跃会话阻止删除，请先撤销会话 |
| `AGENT_EXPIRED` | 403 | 临时账号已过期 |

### 身份源

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/identity-sources` | 查询或注册外部身份源 |
| GET, PUT | `/admin/identity-sources/{sourceId}/mappings` | 查询或替换主体映射 |
| DELETE | `/admin/identity-sources/{sourceId}/mappings/{subjectType}/{externalId}` | 删除一条映射 |

读取需要 `identity.read` 权限，写入需要 `identity.write` 权限。`kind` 只是协议类别
（`ldap`、`oidc` 或 `directory`）；产品专用的连接器形态由部署侧在 `config` 内表达，不属于
协议词表。`config` 必须是纯 JSON 对象，且不得包含 `password`、`secret`、`token`、`apikey`、
`clientsecret`、`bindpassword` 等秘密键；秘密材料只能以凭证库引用表达。已存储的 `config`
不会在任何读取响应中回显。

两个列表端点均使用 `cursor` 和 `limit` 分页；映射列表还接受可选的 `subjectType` 过滤
（`user` 或 `team`）。列表响应返回的 `nextCursor` 在最后一页为 `null`。

错误码：

| 错误码 | 状态 | 含义 |
| --- | --- | --- |
| `INVALID_IDENTITY_SOURCE` | 400 | id、kind 或显示名无效；或 `config` 不是 JSON 对象、包含秘密键 |
| `IDENTITY_SOURCE_EXISTS` | 409 | 身份源 id 已存在 |
| `INVALID_IDENTITY_MAPPING` | 400 | 主体类型、外部 id 或本地主体 id 缺失或无效 |
| `RESOURCE_NOT_FOUND` | 404 | 身份源不存在 |

### 数据范围

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/data-scope-rules` | 查询或创建数据范围规则 |
| GET, DELETE | `/admin/data-scope-rules/{ruleId}` | 读取或删除单条规则 |
| GET | `/admin/data-scope/context` | 解析单个用户的检索上下文 |

读取需要 `data_scope.read` 权限，写入需要 `data_scope.write` 权限。规则以
`management_scope`、`exception_grant` 或 `explicit_deny` 之一的规则种类把主体（`user`、
`role` 或 `team`）绑定到资源。`resourceKind` 为协议定义的 `team` 或部署自定义的小写标识符；
协议不枚举部署自定义种类，并将其视为不透明引用。`startsAt` 和 `expiresAt` 标记临时窗口，
规则到期即停止授权。

`GET /admin/data-scope/context?userId=...` 基于已存储的绑定和规则推导单个用户的检索上下文：
`orgScope` 是用户所属团队的子树，`allowedResources` 和 `deniedResources` 是 `(kind, id)`
引用（除 `team` 外 kind 均由部署定义），显式拒绝优先于允许。已停用账号不是有效主体：该接口
以 `USER_DISABLED`（403）拒绝，而不会推导其已存储的授权。

规则列表使用 `cursor` 和 `limit` 分页，返回的 `nextCursor` 在最后一页为 `null`。

错误码：

| 错误码 | 状态 | 含义 |
| --- | --- | --- |
| `INVALID_DATA_SCOPE_RULE` | 400 | id、规则种类、主体或资源无效 |
| `DATA_SCOPE_RULE_EXISTS` | 409 | 规则 id 已存在 |
| `USER_REQUIRED` | 400 | 缺少必需的 `userId` 查询参数 |
| `USER_DISABLED` | 403 | 指定账号已停用，不推导上下文 |
| `RESOURCE_NOT_FOUND` | 404 | 规则或用户不存在 |

### Skill

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET, POST | `/admin/skills` | 查询或创建 Skill 元数据 |
| GET, PATCH, DELETE | `/admin/skills/{skillId}` | 读取、更新或删除 Skill |
| POST | `/admin/skills/{skillId}/versions` | 使用 multipart 上传 ZIP |
| POST | `/admin/skills/{skillId}/versions/{version}/publish` | 发布版本 |
| DELETE | `/admin/skills/{skillId}/versions/{version}` | 撤回版本 |
| GET, POST | `/admin/skill-assignments` | 查询或创建授权关系 |
| DELETE | `/admin/skill-assignments/{assignmentId}` | 删除授权关系 |
| PATCH | `/admin/skill-assignments/{assignmentId}` | 修改授权的到期时间（`expiresAt` 为 RFC3339，`null` 表示永久） |

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

`endpoint` 不得内联凭据：URL 中带 userinfo 会以 400 `INVALID_MODEL` 拒绝，
密钥应通过 `credentialId` 引用。endpoint 在审计中的记录方式见
[管理操作审计](#管理操作审计)。

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
| POST | `/admin/data-plane/publish` | 从模型目录派生网关路由并原子替换期望状态 |
| GET, PUT | `/admin/data-plane/desired-state` | 读取期望状态；PUT 是已弃用的手工逃生口，用于运维手工编写的状态 |
| GET | `/admin/data-plane/status` | 读取最近一次调和观测状态及目录漂移比对结果 |

期望状态只引用外部 Secret 名称和键，不包含模型服务商凭证明文。目录派生路由在模型绑定
Credential 时引用约定 Secret `aep-credential-<credentialId>`（键 `api-key`，命名空间
`aep-credentials`），该 Secret 由部署侧 Secret 系统负责供给。命名空间由服务端解析，
客户端发送的 `credentialRef.namespace` 会被忽略。

#### Operator 异步发布（入队阶段）

管控服务已实现事务入队和 operation 查询，以 `AEP_GATEWAY_PUBLICATIONS_ENABLED`
显式开启，默认 `false`；关闭时返回 `503 DATA_PLANE_PUBLICATIONS_DISABLED`。
**本阶段没有 relay 和 operator 观测读取器**，开启后任务保持 `queued`，conditions
均为 Unknown，仅用于隔离环境的入队验收。现有客户端继续使用同步 `POST /publish`
（`200 DataPlaneDesiredState`），两条路径之间不自动回退。relay 故障恢复、operator
独占写及完整链路生效验收完成后，才能切换生产客户端。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| POST | `/admin/data-plane/publications` | 将目录派生路由快照持久入队，返回 `202 DataPlanePublication` 与 `Location` |
| GET | `/admin/data-plane/publications/{operationId}` | 查询投递进度及对应版本的 operator conditions |

两者均沿用数据平面管理的 `data_plane.write` 权限，部署范围取自认证会话。不存在、
已清理或属于其他部署的 operation 统一返回 `404 RESOURCE_NOT_FOUND`。响应使用
`Cache-Control: no-store`，不暴露原始 CR 或凭证明文。本接口**仅发布路由**，不发布
限流配额、不修改模型授权；模型访问授权与配额消耗主体是不同概念。

必须携带 `Idempotency-Key`（1–128 个 ASCII 字母、数字或 `._:-`），请求体为 `{}` 或
`{"revision":"release-1"}`。不得用秘密作为幂等键。revision 去掉首尾空白后比较，
显式空值或已绑定其他路由内容的 revision 返回 `422 INVALID_DATA_PLANE_STATE`；
版本标签不代表顺序。JSON 损坏或幂等键缺失／非法返回 `400`，非法键的错误码为
`INVALID_IDEMPOTENCY_KEY`。校验失败不得提交 operation／outbox 意图。

首次 `202` 仅表示一致的目录快照、operation、审计事件与 outbox 意图已在同一个 DB
事务中提交，不代表 CR 或 Higress 已接受配置。调用方应在发送前保存自生成的幂等键。
同部署、同键、同归一化请求重试，经重新认证授权后返回原 operation 的当前状态（包括
终态），不重新读取目录派生快照、不重复入队。请求不同则返回
`409 IDEMPOTENCY_KEY_CONFLICT`。绑定与 operation 至少保留到 `idempotencyExpiresAt`
（不早于创建后 24 小时），重放不延长该期限；过期键可能产生新任务，不应盲目重试。
响应不确定时，在期限内用原键与原请求重试；已拿到 operationId 后查询该 operation。

入队阶段不自动清理 operation 和快照；`idempotencyExpiresAt` 是最短留存边界，
不是删除时间。现有留存清理器不删除待投递 outbox；幂等键仅保存 SHA-256 摘要，不存原文。

`phase` 仅表示投递生命周期：`queued` → `projecting` → `projected`、`failed` 或
`superseded`。临时投递错误退避重试；`failed` 表示不可恢复的投影错误，携带脱敏
`errorCode`。`projected` 表示 relay 已核对该快照的 CR 写入与删除。新发布可以取代
旧发布（包括已 projected 的发布）；旧 worker 即使延迟恢复，也不得覆盖新意图。
`failed`、`superseded` 是终态。崩溃恢复、顺序与 fencing 需在后续 outbox 实现阶段验证。

响应始终含各一条 `Accepted`、`Applied`、`Ready`，初始均为 `Unknown`。Accepted
表示 operator 语义校验通过；Applied 表示渲染后的 K8s 资源写入；Ready 需要独立证据
证明对应版本已被数据面加载，并覆盖相关路由与副本。每条观测带 `observedRevision`；
没有对应版本证据时为 `null`／`Unknown`。SSA 成功、单次探测成功或旧 `ready` 字段均
不足以判定 Ready。投递 projected 后仍可能校验失败，Applied 为 True 时 Ready 也
可以为 Unknown；conditions 独立于投递阶段演进。本阶段不实现 Ready 检测，也不宣称
完整链路已验证。

参见 [发布 OpenAPI 源文件](../openapi/aep-v1-data-plane-publications.openapi.yaml)
与 [SDK 用法](../packages/aep-sdk-node/README.zh-CN.md#异步路由发布)。

### 部署设置

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/admin/deployment/settings` | 读取部署级运行时设置 |
| PUT | `/admin/deployment/settings` | 部分更新运行时设置 |

`GET` 需要 `deployment.read` 权限，`PUT` 需要 `deployment.write` 权限；内置管理员角色
同时具备两者。每个设置项都会返回已存储的 `override`、经环境变量回落后的
`effectiveValue`，以及生效值来源 `source`（`override`、`env` 或 `unset`）。

首个受管理的设置项是 `modelGatewayBaseUrl`，即服务 metadata 向客户端下发的
OpenAI 兼容模型网关地址。请求体 `{"modelGatewayBaseUrl": "https://gateway.example.com/v1"}`
会写入优先于 `AEP_MODEL_GATEWAY_BASE_URL` 的运行时覆盖；发送
`{"modelGatewayBaseUrl": null}` 会清除覆盖并回落到环境变量值；省略该字段则保持不变。
取值必须是主机名可被客户端访问的绝对 `http`/`https` URL：集群内部主机
（`*.svc.cluster.local` 或无 `.` 的单段裸主机名）会被拒绝，生产部署还会
禁止 localhost 等回环地址。非法取值返回 `422`，错误码为 `INVALID_DEPLOYMENT_SETTINGS`。

三个设置项用于数字员工计算资源治理。`computeDefaultTier` 是员工运行实例的默认资源
档位，写入时会对照计算档位目录校验，未知档位名返回 `422`；
`computeIdleHibernateSeconds`（60-86400）是员工运行实例空闲多久后缩容到零；
`computeConcurrencyCap`（1-10000）限制部署内并发员工运行实例数。与其他设置项一致，
显式 `null` 会清除覆盖并回落到环境变量（`AEP_COMPUTE_DEFAULT_TIER`、
`AEP_COMPUTE_IDLE_HIBERNATE_SECONDS`、`AEP_COMPUTE_CONCURRENCY_CAP`）。整数设置项以
相同的 override/effectiveValue/source 结构返回整数值。

### 计算治理

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/admin/compute/tiers` | 读取部署侧拥有的计算档位目录 |
| GET | `/admin/compute/assignments` | 查询按 Agent 的计算档位分配 |
| PUT | `/admin/compute/assignments/{agentId}` | 设置或清除单个 Agent 的计算档位覆盖 |

两个 `GET` 都需要 `deployment.read` 权限；`PUT` 需要 `agents.compute.write` ——
可委托的数字员工计算资源授权，不能操作真人账号（完整管理员始终具备）。

档位由部署侧拥有，API 只读：控制面从 `AEP_COMPUTE_TIERS_FILE` 指向的 JSON 文件加载
目录（对象包含 `items` 数组，条目为 `name`、可选 `description`、Kubernetes quantity
语法的 `cpuRequest`/`cpuLimit`/`memoryRequest`/`memoryLimit`（如 `250m`、`512Mi`），
以及固定为 `deployment` 的 `source`）。未配置目录文件时 `GET /admin/compute/tiers`
返回空 `items`。

每行分配返回 Agent、已存储的档位 `override`、经部署默认档位回落后的 `effective`，
以及生效档位来源 `source`（`override` 或 `default`），存在覆盖时附带 `updatedAt`
和 `updatedBy`。请求体 `{"tier": "standard"}` 写入覆盖；`{"tier": null}` 清除覆盖并
回落到部署默认档位。写入会记录到部署审计轨迹，并在员工实例下次创建时生效——v1
不就地变更运行中实例。未知 Agent 返回 `404`，不在目录中的档位名返回 `422`。

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

### 管理操作审计

`GET /admin/audit/operations` 要求 `audit.read` 权限，只返回调用者部署内的记录，
按 cursor 倒序排列。支持 `cursor`、`limit`、`action`、`resourceType`、
`resourceId`、`actorUserId`、`createdAfter` 和 `createdBefore`；时间筛选使用
RFC 3339。响应包含 `items` 和可空的 `nextCursor`。

每条记录包含操作人、动作、资源、结果、时间和 `changes`：脱敏的变更前后快照或
兜底捕获的请求字段；没有载荷时为 `null`。创建账号的载荷保留用户名，密码与未知
字段继续打码。URL 字段（模型 `endpoint`）只保留 scheme、host、port 和 path：
query、fragment 与 userinfo 在写入和读取时都会去掉，内联凭据不会进入审计表，
也不会回显给读取方。此只读接口不提供凭据明文。记录按 `AEP_AUDIT_RETENTION` 清理，
配置说明见[生产运行基线](production-runtime.zh-CN.md)。

创建账号——以及批量导入创建的每一个账号——的审计行与账号本身、角色/团队绑定
在同一事务提交。审计行写不进去的账号不会被创建：单条创建返回
`INTERNAL_ERROR`，批量导入把该行记入 `errors` 并保留已审计通过的行。一个账号都没创建的
导入返回 `422 USER_IMPORT_REJECTED`，且不写任何 success 审计行，审计记录里不会出现
没有实际生效的导入。

被拒绝或失败的写操作同样入表：`result` 为 `failure`，`reason` 放稳定错误码。
覆盖范围是到达管理面、且已通过认证的非读取请求（POST、PUT、PATCH、DELETE），
包括被权限检查拒绝的请求——审计包在权限检查外层，因此拒绝也会带操作人和目标
落库。认证之前就被拒的请求（缺少 bearer 令牌、令牌无效或已撤销、临时密码未改）
没有可归属的操作人，仍归认证审计；读取请求不记。该链路始终是尽力而为：审计写
失败只记日志，不改变已返回给调用方的响应。

失败记录的 `changes` 在脱敏后的尝试内容之外，还带请求标识与状态码，例如
`{"requestId":"0198...","status":400,"request":{"displayName":"..."}}`；脱敏
规则与成功写入一致。两条规则控制容量：已在事务内自行记录审计的处理函数不会被
兜底路径重复记录；同一操作人、同一目标、同一错误码、同一脱敏内容的重复失败，
每个副本每 30 秒只写一条，不同内容仍各自入表。

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
`RESOURCE_NOT_FOUND`、`VERSION_CONFLICT`、`RATE_LIMITED`、`INVALID_DEPLOYMENT_SETTINGS`
和 `INTERNAL_ERROR`。

安全的读取请求采用指数退避重试。事件批次依据 `eventId` 幂等。授权码交换和凭证获取不得
盲目重试。收到 `429` 或 `503` 时遵守 `Retry-After`。

团队 `defaultRoleIds` 配置与有效角色继承规则见[团队默认角色](team-default-roles.zh-CN.md)。

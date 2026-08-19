# AEP v1 API 指南

简体中文 | [English](api-v1.md)

本文说明 AEP v1 HTTPS REST API。机器可读契约位于
[`../openapi/aep-v1.openapi.yaml`](../openapi/aep-v1.openapi.yaml)。

## 1. 通用约定

基础地址：`https://enterprise.example.com/aep/v1`

Agent 请求头：

```http
Authorization: Bearer <access-token>
X-AEP-Agent-ID: <stable-agent-instance-id>
X-AEP-Protocol-Version: 1.0
X-Request-ID: <request-id>
```

JSON 字段使用 `camelCase`，时间使用 RFC 3339 UTC，错误使用 RFC 9457
`application/problem+json`。AEP v1 使用 REST 轮询，不使用 SSE 或 WebSocket。

## 2. 端点总览

### 服务与认证

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/metadata` | 查询 AEP 版本和服务能力 |
| POST | `/auth/exchange` | 交换一次性授权码 |
| POST | `/auth/refresh` | 刷新会话 |
| POST | `/auth/logout` | 撤销当前 refresh 会话 |

### Agent API

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/agent/me` | 查询当前用户和企业身份 |
| GET | `/agent/skills/manifest` | 获取完整的期望 Skill 清单 |
| GET | `/agent/skills/{skillId}/versions/{version}/package` | 下载 Skill ZIP 包 |
| POST | `/agent/skills/sync-results` | 上报 Skill 同步结果 |
| POST | `/agent/events/batch` | 幂等批量上传事件 |
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

### `POST /auth/exchange`

```json
{
  "authorizationCode": "one-time-code",
  "redirectUri": "zhiyuan://auth/callback",
  "agentId": "0198a910-5235-7b24-9b63-4b7dd46782e0",
  "agentVersion": "1.8.0",
  "platform": "windows"
}
```

```json
{
  "accessToken": "eyJ...",
  "refreshToken": "refresh-token",
  "tokenType": "Bearer",
  "expiresIn": 7200
}
```

授权码只能使用一次。

### `POST /auth/refresh`

请求：

```json
{"refreshToken": "refresh-token", "agentId": "0198a910-5235-7b24-9b63-4b7dd46782e0"}
```

响应使用上述 token 结构。响应中出现新的 refresh token 时，客户端必须替换旧值。

### `POST /auth/logout`

请求：`{"refreshToken":"refresh-token"}`。成功返回 `204 No Content`。

## 4. 当前身份

### `GET /agent/me`

```json
{
  "user": {"id": "user_123", "displayName": "李明", "email": "liming@example.com"},
  "enterprise": {"id": "enterprise_001", "name": "示例企业"},
  "roles": ["employee"],
  "sessionExpiresAt": "2026-08-19T10:00:00Z"
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

## 6. 事件上传

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

## 7. 凭证

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

## 8. 模型

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

对于远程模型，Agent 使用身份凭证访问模型描述中声明的网关地址，网关逐次检查权限。
AEP 不重新定义推理请求和响应格式。

## 9. 管理端 API

管理端端点必须使用管理员身份。

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

### 事件

`GET /admin/events` 支持 `cursor`、`limit`、`userId`、`agentId`、`type`、
`resourceType`、`resourceId`、`result`、`occurredAfter` 和 `occurredBefore`。

## 10. 错误与重试

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

# Agent Enterprise Protocol v1

简体中文 | [English](aep-v1.md)

状态：初始草案
协议版本：`1.0`
最后更新：2026-08-19

## 1. 目的

Agent Enterprise Protocol（AEP）定义企业托管的 Agent 客户端与企业服务之间的
REST API 契约。它为 Agent SDK 提供稳定的集成边界，同时将甲方特定规则保留在服务端。

## 2. 范围

AEP v1 定义：

1. 用户身份识别与 Agent 会话交换；
2. 托管 Skill 同步；
3. Agent 事件上传；
4. API 凭证授权及客户端下发；
5. 模型目录发现与访问权限判定；
6. 上述资源的管理端 API。

AEP v1 不定义模型推理数据格式、MCP 调用、Agent 间任务协作、设备证明、签名策略工件
或甲方特定业务规则。

## 3. 参与方

| 参与方 | 职责 |
| --- | --- |
| Agent 客户端 | 展示 Agent 功能并应用企业托管状态 |
| 企业 Agent SDK | 在 Agent 主进程内实现 AEP |
| 管控服务 | 管理身份、授权关系、Skill 元数据和模型权限 |
| 资产服务 | 存储和提供 Skill 包及可选用户产物 |
| 事件服务 | 接收并索引 Agent 事件 |
| 模型网关 | 强制执行远程模型权限并调用模型服务商 |
| 身份提供方 | 认证用户并提供授权码 |

管控、资产和事件服务可以部署为一个模块化服务。

## 4. 客户端边界

对于 Electron 客户端，AEP SDK 必须运行在主进程，Renderer 通过 IPC 访问 SDK。

```text
Renderer UI
    | IPC
    v
Electron 主进程 / 企业 Agent SDK
    | HTTPS REST API
    v
企业服务端
```

Token 和可下发到客户端的凭证不得暴露给 Renderer。网关使用的模型服务商凭证必须保留
在服务端。

## 5. REST 传输协议

AEP 的数据交互统一使用 HTTPS REST API。

- 生产环境必须使用 HTTPS。
- 资源 URL 统一位于 `/aep/v1` 下。
- `GET` 读取资源，不得改变业务状态。
- `POST` 创建资源或执行明确命令。
- `PATCH` 局部更新资源。
- `DELETE` 撤回或删除资源。
- JSON 使用 UTF-8 编码的 `application/json`。
- 错误使用 RFC 9457 `application/problem+json`。
- Skill 包使用 `application/zip`。
- 时间使用 RFC 3339 UTC。
- 标识符是不可推断语义的不透明字符串。

AEP v1 不使用 SSE、WebSocket 或自定义长连接。客户端通过带条件请求的 REST 轮询发现
变更。模型流式输出可以使用模型 API 自身的协议，但不属于 AEP。

## 6. 请求头

经过认证的 Agent 请求包含：

```http
Authorization: Bearer <access-token>
X-AEP-Agent-ID: <stable-agent-instance-id>
X-AEP-Protocol-Version: 1.0
X-Request-ID: <request-id>
```

`X-AEP-Agent-ID` 用于识别安装实例，但不是安全凭证。服务端必须从 access token 推导用户
和企业身份。服务端应回传 `X-Request-ID`；客户端未提供时应生成一个。

条件请求使用标准 HTTP 请求头，例如 `ETag` 和 `If-None-Match`。

## 7. 身份认证

标准登录顺序为：

```text
Agent 打开企业登录页面
  -> 身份提供方认证用户
  -> Agent 获得一次性授权码
  -> Agent 通过 REST API 交换授权码
  -> 服务端返回 access token 和 refresh token
```

access token 用于普通请求，refresh token 仅用于刷新接口。退出登录时撤销 refresh 会话。
当前身份接口是客户端展示用户和企业信息的唯一可信来源。

## 8. Skill 同步

### 8.1 期望清单

服务端返回当前用户完整的期望 Skill 清单。每一项包含稳定 ID、版本、包地址、SHA-256
摘要、大小和启用状态。

响应包含不透明的修订号和 `ETag`。Agent 使用 `If-None-Match` 定时轮询；收到
`304 Not Modified` 时无需执行同步。

### 8.2 状态收敛

清单发生变化后，Agent：

1. 下载缺失或发生变化的包；
2. 校验 SHA-256 摘要；
3. 安装到托管 Skill 目录；
4. 删除完整清单中已不存在的托管 Skill；
5. 刷新运行时 Skill 清单；
6. 通过 REST API 上报同步结果。

只有通过 AEP 安装的 Skill 属于同步管理范围。Agent 不得删除非托管 Skill 或无关用户文件。

### 8.3 包格式

Skill 包是 ZIP 文件，根目录必须包含 `SKILL.md`，可以包含脚本、参考资料和资源。包内路径
必须为相对路径，且不得逃逸解压目录。清单中的摘要针对资产服务返回的完整 ZIP 字节计算。

### 8.4 删除语义

- 取消 Skill 授权会使其从对应主体的期望清单中消失。
- 撤回版本会阻止后续下载该版本。
- 删除 Skill 会使其从后续所有清单中消失。
- 已下发并被复制为非托管副本的内容无法保证被删除。

## 9. 事件上传

Agent 通过 REST API 批量上传事件。每个事件拥有全局唯一的 `eventId`，服务端依据该值去重，
因此客户端可以安全重试。

每批最多 100 个事件，建议不超过 1 MiB。服务端分别返回已接受和被拒绝的事件 ID。
Agent 将可重试失败保留在本地 outbox 中。

标准事件类型包括 `auth.login`、`auth.logout`、`skill.sync.started`、
`skill.installed`、`skill.updated`、`skill.removed`、`skill.sync.failed`、
`credential.resolved`、`credential.resolve_failed`、`model.request.completed`、
`model.request.failed` 和 `agent.heartbeat`。

事件元数据不得包含 token、API Key 或完整模型输入输出，除非另有企业策略明确启用。

## 10. API 凭证

AEP 定义两种下发模式：

| 模式 | 行为 |
| --- | --- |
| `server_only` | 凭证保留在服务端，由网关使用 |
| `agent` | 获得授权的 Agent 可以通过 REST API 获取凭证 |

模型服务商 Key 应使用 `server_only`。凭证列表只返回元数据和掩码值。明文凭证仅由解析接口
返回，并携带 `Cache-Control: no-store`。

禁用或删除凭证会阻止后续获取。服务端无法保证已经下发到不可信客户端的凭证被彻底删除。

## 11. 模型与权限

模型目录只包含当前用户可见的模型。模型描述包括 ID、显示名称、来源类型、调用协议、服务地址
或本地引用、能力和启用状态。

来源类型包括：

| 来源类型 | 含义 |
| --- | --- |
| `gateway` | 通过模型网关访问的商业或托管模型 |
| `enterprise_open_source` | 企业托管的开源模型 |
| `local` | 在 Agent 设备本地运行的模型 |

模型网关必须在每次远程推理请求时再次检查权限，仅过滤模型目录不足以形成服务端授权。本地模型
权限属于产品功能控制，无法对抗完全控制本机的用户。

AEP 不重新定义模型推理数据。模型描述中的 `protocol` 指示 SDK 使用哪种模型客户端。

## 12. 管理端

管理端 API 管理 Skill 及其版本、用户或角色授权、凭证及轮换、模型描述及授权，以及事件查询。
管理端必须使用管理员身份，不能仅凭普通 Agent 用户会话访问。

## 13. 错误模型

错误遵循 RFC 9457：

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

标准错误码包括 `INVALID_REQUEST`、`TOKEN_INVALID`、`ACCESS_DENIED`、
`RESOURCE_NOT_FOUND`、`VERSION_CONFLICT`、`RATE_LIMITED`、
`SKILL_NOT_ASSIGNED`、`CREDENTIAL_NOT_DELIVERABLE`、`MODEL_NOT_ALLOWED` 和
`INTERNAL_ERROR`。

## 14. 兼容性

主版本号出现在基础路径中。新增可选字段和端点不改变主版本，客户端必须忽略未知 JSON 字段。
破坏性变更必须使用新路径，例如 `/aep/v2`。

元数据接口公布服务端支持的版本范围。不受支持的客户端收到 `426 Upgrade Required` 和
AEP Problem Details。

## 15. 最低安全基线

AEP v1 不要求签名策略工件或设备证明，但仍要求：

- 除本地开发外使用 HTTPS；
- 依据经过认证的身份执行授权；
- 每次包下载、凭证获取和远程模型调用均执行服务端授权；
- 对可获取的凭证明文进行加密存储；
- 日志和事件中隐藏敏感值；
- 解压 Skill 前校验归档路径；
- 限制请求和事件批次大小；
- 用户退出或账号禁用时撤销 refresh 会话。

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
3. Agent 遥测事件上传；
4. 通过心跳发现并可靠投递具有不同作用域的管控事件；
5. API 凭证授权及客户端下发；
6. 模型目录发现与访问权限判定；
7. 上述资源的管理端 API。

AEP v1 不定义模型推理数据格式、MCP 调用、Agent 间任务协作、设备证明、签名策略工件
或甲方特定业务规则。

## 3. 参与方

| 参与方 | 职责 |
| --- | --- |
| Agent 客户端 | 展示 Agent 功能并应用企业托管状态 |
| 企业 Agent SDK | 在 Agent 主进程内实现 AEP |
| 管控服务 | 管理身份、授权关系、Skill 元数据和模型权限 |
| 资产服务 | 存储和提供 Skill 包及可选用户产物 |
| 事件服务 | 接收遥测事件并向 Agent 投递作用域管控事件 |
| 模型网关 | 强制执行远程模型权限并调用模型服务商 |
| 身份服务 | 认证知远平台账号并适配甲方身份提供方 |

管控、资产和事件服务可以部署为一个模块化服务。

## 4. 客户端边界

对于 Electron 客户端，AEP SDK 必须运行在主进程，Renderer 通过 IPC 访问 SDK。

```text
Renderer UI
    | IPC
    v
Electron 主进程 / 企业 Agent SDK
    | HTTP(S) REST API
    v
企业服务端
```

Token 和可下发到客户端的凭证不得暴露给 Renderer。网关使用的模型服务商凭证必须保留
在服务端。

## 5. REST 传输协议

AEP 的数据交互统一使用 HTTP 或 HTTPS REST API。

- 开发和生产环境均可使用 HTTP 或 HTTPS，AEP 实现不得仅因部署阶段为生产环境而拒绝 HTTP。
- 账号密码或 bearer token 经过不可信网络时强烈建议使用 HTTPS。选择 HTTP 时，应使用可信
  内网、专线或 AEP 之外的传输保护。
- 本地开发不得强制配置 TLS。使用明文 HTTP 进行密码登录时，
  应仅限回环地址或隔离的测试网络。
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

AEP v1 不使用 SSE、WebSocket 或自定义长连接。客户端通过心跳标志、管控事件轮询和
资源条件轮询发现变更。模型流式输出可以使用模型 API 自身的协议，但不属于 AEP。

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

服务端按企业返回可用登录方式。AEP v1 支持：

| 方式 | 行为 |
| --- | --- |
| `password` | Agent 将知远平台账号密码提交给身份服务 |
| `federated` | Agent 使用系统浏览器进入甲方 OIDC 或服务端自定义适配器，再交换一次性授权码 |

知远平台账号由管理员逐个创建或批量导入，AEP v1 不提供公开自助注册。密码必须使用自适应
密码哈希存储，管理员也不得读取原密码。

上游支持 OIDC 时，联合登录使用 Authorization Code + PKCE。甲方账号密码只在甲方登录
页面输入，不得经过 Agent 或 AEP 密码登录接口。其他甲方登录系统可以通过服务端适配器
接入，只要最终签发相同的短生命周期、单次使用交换码。

两种登录方式最终建立相同的 AEP 会话，并返回：

- 调用管理 API 的 AEP access token；
- 用于会话续期的 refresh token；
- 调用模型网关的 model access token。

model access token 在登录和刷新时签发，包含明确的模型网关 audience、有效期、企业身份、
用户身份和模型授权声明。有效期内，Agent 直接向模型网关出示该凭证；模型网关使用受信任
签名密钥在本地完成校验，不得为每次推理请求同步调用管控服务重新做授权决策。

这不等于取消逐请求认证：网关仍须在每次请求中校验凭证签名、audience、有效期及目标模型
范围。权限变更最迟在凭证到期时生效；需要更快收敛时，可以投递
`model.catalog.changed` 管控事件并要求刷新会话。

access token 用于普通请求，refresh token 仅用于刷新接口。退出登录时撤销 refresh 会话。
刷新会同时轮换 AEP token 与模型 token。退出登录或禁用账号会撤销 refresh 会话，已签发
的短期 token 则由有效期限制。当前身份接口是客户端展示用户和企业信息的唯一可信来源。

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

## 9. 事件

AEP 明确区分两个事件方向：

| 方向 | 名称 | 用途 |
| --- | --- | --- |
| Agent 到服务端 | 遥测事件 | 上报 Agent 活动和执行结果 |
| 服务端到 Agent | 管控事件 | 通知 Agent 托管状态或任务发生变化 |

### 9.1 遥测事件上传

Agent 通过 REST API 批量上传遥测事件。每个事件拥有全局唯一的 `eventId`，服务端依据
该值去重，因此客户端可以安全重试。

每批最多 100 个事件，建议不超过 1 MiB。服务端分别返回已接受和被拒绝的事件 ID。
Agent 将可重试失败保留在本地 outbox 中。

标准遥测事件类型包括 `auth.login`、`auth.logout`、`skill.sync.started`、
`skill.installed`、`skill.updated`、`skill.removed`、`skill.sync.failed`、
`credential.resolved`、`credential.resolve_failed`、`model.request.completed` 和
`model.request.failed`。

遥测事件元数据不得包含 token、API Key 或完整模型输入输出，除非另有企业策略明确启用。

### 9.2 管控事件作用域

管控事件支持 `global`、`team` 和 `user` 三种作用域。服务端根据经过
认证的身份和已注册 Agent 实例计算适用作用域，客户端不得自行选择所属组织或用户。

全局、组织或个人事件必须为每个适用 Agent 分别维护投递状态，事件本身不存在共享的
`consumed` 标志。

### 9.3 发现与投递

Agent 通过 REST API 发送心跳，响应只包含待处理标志和服务端水位。标志为 true 时，Agent
调用管控事件查询接口。

读取事件不代表消费。Agent 必须先将事件持久化到本地收件箱，再确认状态为 `received`。
服务端会重新投递尚未确认的事件。Agent 执行完成后，再独立上报 `succeeded` 或 `failed`。

投递状态包括 `pending`、`received`、`running`、`succeeded`、`failed`、`expired` 和
`superseded`。接收确认与结果上报必须幂等。

### 9.4 事件作为失效通知

管控事件应作为轻量变更通知，而不是托管数据本身。例如 `skill.manifest.changed` 通知
Agent 重新获取最新 Skill 清单并收敛状态。事件中不得包含凭证明文、完整 Skill 包或大型
配置对象。

标准映射包括：

| 事件类型 | Task |
| --- | --- |
| `skill.manifest.changed` | 拉取并同步 Skill 清单 |
| `plugin.manifest.changed` | 在支持插件管理时拉取并同步插件清单 |
| `credential.assignments.changed` | 刷新凭证授权清单 |
| `model.catalog.changed` | 刷新模型目录 |

详细 REST 契约见 API 指南和管控事件 OpenAPI 文档。

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

Agent 使用登录或刷新时取得的 model access token 进行远程推理。模型网关每次请求都在本地
校验该 token 的签名、有效期、audience 和模型授权范围，但不得为每次模型调用同步请求管控
服务。仅过滤模型目录不足以形成服务端约束，而本地 token 校验也避免在推理链路增加一次
控制面往返。本地模型权限属于产品功能控制，无法对抗完全控制本机的用户。

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

- 同时支持 HTTP 与 HTTPS 部署，不在协议层强制 HTTPS；
- 使用 HTTP 时明确接受凭证可能被链路窃取的风险，并强烈建议使用可信内网、专线或外层
  传输保护；
- 依据经过认证的身份执行授权；
- 每次包下载和凭证获取均执行服务端授权；
- 每次远程模型调用在模型网关本地校验登录时签发的 model access token，不同步调用管控服务；
- 对可获取的凭证明文进行加密存储；
- 日志和事件中隐藏敏感值；
- 解压 Skill 前校验归档路径；
- 限制请求和事件批次大小；
- 用户退出或账号禁用时撤销 refresh 会话。

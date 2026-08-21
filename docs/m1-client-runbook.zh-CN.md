# AEP M1 Node Agent 模型运行手册

M1 将 Node 示例 Agent 接入 Higress 的 OpenAI 兼容模型数据面，但不把推理逻辑放入 SDK Core。Agent 通过 `AepClient.getModelConnection()` 取得网关地址和短期模型令牌，再使用采用 Apache-2.0 许可证的 [OpenAI 官方 Node SDK](https://developers.openai.com/api/docs/libraries)。

## 客户端流程

```text
Agent -> AEP 登录/模型发现 -> SQLite token store
      -> 携带 AEP model JWT 的 OpenAI 客户端 -> authorizer -> Higress -> 供应商
      -> 模型遥测 -> SQLite outbox -> AEP 批量上传
```

`AEP_MODEL_ID` 用于选择显式授权模型；未设置时使用默认模型。普通和流式 Chat Completions 均受支持。遥测只记录模型 ID、是否流式、耗时、安全的 HTTP 状态和可用的 token 计数，不记录 prompt、模型令牌、网关地址、供应商凭证或供应商错误详情。

## 自动演示

需要 Node.js 24、Go 1.26、Docker Desktop 和 Docker Compose。

```bash
npm ci
npm run test:e2e:m1-client
```

测试会构建 SDK Core 和 Agent，启动完整 M1 服务，创建模型授权，并把构建后的 Agent 作为真实子进程启动。验收覆盖普通与流式推理、失败遥测、供应商密钥隔离、outbox 上传和管理员查询；结束后自动删除容器及数据卷。

完整回归：

```bash
npm run check
go test ./...
go vet ./...
go build ./...
npm run test:e2e
```

## 手工运行 Agent

先按 [Higress 网关运行手册](m1-gateway-runbook.zh-CN.md)启动并配置服务。账号和 `enterprise-chat` 授权准备完成后运行：

```powershell
$env:AEP_BASE_URL = 'http://localhost:8080'
$env:AEP_ENTERPRISE_ID = 'demo'
$env:AEP_USERNAME = 'client-user'
$env:AEP_PASSWORD = 'temporary-password-123'
$env:AEP_AGENT_ID = 'demo-node-agent'
$env:AEP_AGENT_DATA_DIR = '.aep-agent-demo'
$env:AEP_CHAT_PROMPT = 'Hello from the managed Agent'
npm run start --workspace @aep/example-node-agent -- chat
```

预期输出：

```json
{"modelId":"enterprise-chat","responseModel":"mock-upstream-chat","content":"Hello AEP","streamed":false}
```

流式调用设置 `AEP_MODEL_ID=enterprise-chat` 和 `AEP_CHAT_STREAM=true`。`AEP_MODEL_TIMEOUT_MS` 默认值为 `120000`。通过 `aepctl audit --agent-id demo-node-agent` 查询遥测。

## 职责边界

`@aep/sdk-node` 继续只负责 AEP 管控通信与会话，推理保留在 Agent 中。供应商密钥只保存在 Higress；Agent 仅获得有过期时间且权限限定为已授权逻辑模型 ID 的 AEP model JWT。

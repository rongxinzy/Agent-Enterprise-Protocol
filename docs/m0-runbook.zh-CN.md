# AEP M0 运行手册

简体中文 | [English](m0-runbook.md)

## 范围

M0 提供可独立运行的企业管控闭环：`aepctl` 管理用户和 Skill，Node 示例
Agent 通过 SDK 认证，收敛托管 Skill，持久化处理管控事件，上报遥测，管理员可
查询最终状态。

M0 不提供生产模型网关、模型调用、Credential、MCP、Plugin、A2A、DLP、配额
或策略工件下发。认证响应仍包含可验证的模型访问 JWT，但 M0 组件不会消费它。

## 前置条件

- Node.js 24 和 npm
- Go 1.26
- Docker 与 Compose v2
- 本机端口 `8080`、`9001` 可用，或设置自定义 `AEP_PORT`、
  `AEP_MINIO_CONSOLE_PORT`

安装依赖并运行非容器检查：

```bash
npm ci
npm run check
go test ./...
go vet ./...
go build ./...
```

`npm run check` 会校验并组合 OpenAPI、重新生成 SDK 类型、构建所有 Node
workspace，并运行 SDK 与示例 Agent 测试。

## 启动服务

```bash
npm run compose:up
```

默认本地地址：

- 管控服务：`http://localhost:8080`
- MinIO 控制台：`http://localhost:9001`
- 健康检查：`http://localhost:8080/healthz`

仅供本地开发的初始化身份为企业 `demo`、用户 `admin`、密码
`change-this-admin-password`。Compose 还包含固定的开发签名密钥和 MinIO
凭证。将服务暴露到开发者本机以外之前，必须全部替换。

如需修改服务端口，在启动 Compose 前设置 `AEP_PORT`；MinIO 控制台端口使用
`AEP_MINIO_CONSOLE_PORT`。`GOPROXY` 可覆盖容器构建使用的 Go 模块代理。

## 管理 M0

非一次性演示环境应优先使用 `AEPCTL_PASSWORD`，不要把密码放进命令行。下面
为兼容不同 shell，示例仍使用 `--password`。

```bash
go run ./cmd/aepctl --password change-this-admin-password metadata
go run ./cmd/aepctl --password change-this-admin-password user create --user agent-user --display-name "Agent User" --temporary-password change-this-user-password --require-password-change=false
go run ./cmd/aepctl --password change-this-admin-password user list
```

创建一个根目录含 `SKILL.md` 的 ZIP，然后把命令返回的用户 ID、授权 ID 用于
后续操作：

```bash
go run ./cmd/aepctl --password change-this-admin-password skill create --skill-id review --name Review --description "Managed review Skill"
go run ./cmd/aepctl --password change-this-admin-password skill upload --skill-id review --version 1.0.0 --file ./review.zip
go run ./cmd/aepctl --password change-this-admin-password skill publish --skill-id review --version 1.0.0
go run ./cmd/aepctl --password change-this-admin-password skill assign --skill-id review --subject-type user --subject-id USER_ID
go run ./cmd/aepctl --password change-this-admin-password event publish --scope-type agent --scope-id AGENT_ID --skill-id review --revision 1
```

示例 Agent 读取 `AEP_BASE_URL`、`AEP_ENTERPRISE_ID`、`AEP_USERNAME`、
`AEP_PASSWORD`、`AEP_AGENT_ID`，以及可选的 `AEP_AGENT_DATA_DIR`。执行
`npm run build` 后，运行一次收敛周期：

```bash
node examples/node-agent/dist/index.js once
```

查询闭环记录：

```bash
go run ./cmd/aepctl --password change-this-admin-password agent show --agent-id AGENT_ID
go run ./cmd/aepctl --password change-this-admin-password event deliveries --event-id EVENT_ID
go run ./cmd/aepctl --password change-this-admin-password audit --agent-id AGENT_ID
```

## 端到端验收

```bash
npm run test:e2e
```

脚本使用隔离项目名 `aep-m0-e2e`、服务端口 `18080`、MinIO 控制台端口
`19001`。它通过 `aepctl` 创建用户、Skill、版本、发布和授权；运行 Agent；
验证安装与撤销；检查 delivery、Agent 和遥测记录；最后只删除自己的容器与卷。
可通过 `AEP_E2E_PORT` 和 `AEP_E2E_MINIO_CONSOLE_PORT` 覆盖两个宿主机端口。

排障时运行 `docker compose -f deploy/compose/compose.yaml ps` 和
`docker compose -f deploy/compose/compose.yaml logs control-service`。使用
`npm run compose:down` 停止手工启动的环境；命名数据卷会保留。

## 发布检查

- OpenAPI lint 与 bundle 通过，生成的 SDK 类型没有漂移。
- SDK 构建和 Mock 契约测试先通过，Go 阶段才开始。
- 示例 Agent 构建、SQLite 恢复、ZIP 路径逃逸拒绝和哈希校验测试通过。
- `go test ./...`、`go vet ./...`、`go build ./...` 全部通过。
- PostgreSQL 与 MinIO Compose E2E 通过，并清理隔离资源。
- 针对目标环境复核初始化密码、签名密钥、issuer、MinIO 凭证和 HTTP 暴露。
- M0 metadata 不得宣称支持未实现的网关、Credential、MCP 或 Plugin 能力。

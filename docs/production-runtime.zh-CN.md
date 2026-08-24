# AEP 生产运行基线

该基线使 AEP control-service 与 gateway-authorizer 能够由生产编排平台可靠运行，但不会把本地 Compose 或 `higress-standalone` 包装成生产拓扑。生产环境仍需使用外部托管 PostgreSQL、S3 兼容 MinIO、Higress Helm 部署、TLS 入口、Secret 管理、监控、备份，以及符合企业可用性目标的基础设施。

## 配置门禁

设置 `AEP_ENVIRONMENT=production` 后，control-service 会拒绝临时 JWT 签名密钥、开发 PostgreSQL URL、默认 MinIO 凭据、默认或过短的初始管理员密码。非法布尔值、时长、URL、日志参数、请求限制和 Header 限制都会导致启动失败，不再静默回退。

Mock 联合认证只属于开发和测试夹具。生产环境默认关闭，并拒绝 AEP_ENABLE_MOCK_FEDERATED_AUTH=true。在真实企业身份适配器接入前，不得公布或暴露 federated_auth。

部署输入参考 [control-service.env.example](../deploy/production/control-service.env.example) 与 [gateway-authorizer.env.example](../deploy/production/gateway-authorizer.env.example)。敏感变量支持 `VARIABLE_FILE` 文件路径，直接值与 `_FILE` 形式不能同时设置。Credential keyring 继续使用 `AEP_CREDENTIAL_MASTER_KEY_FILE`，以便受控轮换期间保留旧解密密钥。数据面 reconciler token 也支持文件形式。

签名 seed、Credential keyring、数据库凭据、对象存储凭据和初始管理员密码必须由编排平台的 Secret Provider 提供，不得写入镜像、ConfigMap、Git、Helm values 或 shell 历史。

## 密码认证

密码必须包含 12 至 1024 个 Unicode 字符，并使用 Argon2id 存储。临时密码会话在完成改密前由服务端限制，所签发的 model token 不包含任何模型作用域。登录失败按不透明的主体哈希记录到 PostgreSQL，因此不同来源和多个 control-service 副本共享渐进退避状态；来源只作为独立的不透明审计哈希保留。部署方可通过 `AEP_LOGIN_FAILURE_LIMIT`、`AEP_LOGIN_FAILURE_WINDOW`、`AEP_LOGIN_BACKOFF_BASE` 和 `AEP_LOGIN_BACKOFF_MAX` 按威胁模型调节参数。

认证审计记录包含企业、Agent 标识及不透明的主体/来源哈希，但不会包含用户名、密码、Token 或请求体。部署方应为 `authentication_audit_events` 设置组织认可的保留策略。管理员重置密码或禁用账号会撤销全部 refresh session；已经签发的 access/model JWT 按配置的短 TTL 到期。

## 运行端点

| 端点 | 语义 | 编排用途 |
| --- | --- | --- |
| `/livez` | 进程仍能提供 HTTP，不检查依赖 | Liveness probe |
| `/readyz` | 管控服务检查 PostgreSQL 与 MinIO；网关检查可信 JWKS 可刷新 | Readiness probe |
| `/healthz` | `/readyz` 的兼容别名 | 现有集成 |
| `/metrics` | Prometheus/OpenMetrics，包含稳定路由、方法、状态、延迟和并发数 | 内网监控采集 |

指标不会使用企业、用户、Agent、资源 ID、请求 ID、查询串或 Token 作为 label。访问日志只记录请求 ID、方法、稳定路由、状态、响应字节数与耗时。生产默认 JSON 日志，不记录 Authorization、请求体、查询串、Credential 明文或模型 Prompt。

两个 distroless 镜像均提供内置探针命令：

```sh
/aep-control healthcheck http://127.0.0.1:8080/readyz
/aep-gateway-authorizer healthcheck http://127.0.0.1:8090/readyz
```

## 可用性与发布

数据库 migration 和初始管理员初始化使用 PostgreSQL advisory lock 串行执行；多个 control-service 副本可同时连接新库或升级库，不会竞争 schema 与 bootstrap 写入。MinIO bucket 首次并发创建也可安全收敛。

当依赖服务达到相同可用性目标时，control-service 与 gateway-authorizer 应至少各部署两个跨故障域副本。发布顺序：

1. 备份 PostgreSQL、Skill bucket、Ed25519 签名 seed 和完整 Credential keyring。
2. 启动一个新 control-service 副本并等待 `/readyz`。
3. 滚动更新其余 control-service，再更新 gateway-authorizer。
4. 检查错误率与延迟指标，并验证 Agent 登录、Credential 解析、Skill 下载和模型调用。
5. 观察窗口结束前保留上一版本镜像 digest。

数据库 migration 只向前执行。仅当旧二进制兼容新 schema 时才能直接回滚应用；否则必须恢复同一恢复点的 PostgreSQL 与 MinIO，并恢复匹配的签名 seed 和 Credential keyring。

## 备份恢复

使用维护窗口或协调存储快照，使 PostgreSQL 与 Skill bucket 属于同一恢复点。PostgreSQL 保存身份、授权、Credential 密文、事件、审计和对象引用；MinIO 保存不可变 Skill ZIP。签名 seed 和所有 Credential keyring 条目必须由 Secret 系统独立备份，旧密钥丢失会导致对应 Credential 无法解密。

先恢复到隔离的 PostgreSQL 与 MinIO，校验对象数量和数据库完整性，再只启动一个 control-service 运行内嵌 migration。验证 `/readyz`、JWKS 连续性、Credential 解析审计和 Skill checksum 后，才能增加副本或切换流量。

## 验证

```sh
npm run test:e2e:runtime
```

该场景验证首次并发启动、依赖感知 readiness、独立 liveness、Prometheus 指标、结构化日志、容器权限收敛和 SIGTERM 零退出。完整发布门仍为 `npm run test:e2e`。

Kubernetes、Higress Helm、TLS、RBAC、External Secrets 与在线数据面收敛基线见 [production-data-plane.zh-CN.md](production-data-plane.zh-CN.md)。

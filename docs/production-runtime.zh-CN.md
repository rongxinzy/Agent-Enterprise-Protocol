# AEP 生产运行基线

该基线使 AEP control-service 与 gateway-authorizer 能够由生产编排平台可靠运行，但不会把本地 Compose 或 `higress-standalone` 包装成生产拓扑。生产环境仍需使用外部托管 PostgreSQL、S3 兼容 RustFS、Higress Helm 部署、TLS 入口、Secret 管理、监控、备份，以及符合企业可用性目标的基础设施。

## 配置门禁

设置 `AEP_ENVIRONMENT=production` 后，control-service 会拒绝临时 JWT 签名密钥、开发 PostgreSQL URL、默认 RustFS 凭据、默认或过短的初始管理员密码，并要求配置 License 公钥文件、License 文件、客户 ID 和部署 ID。服务启动时会验证挂载的 License；无效或过期 License 不会启动。非法布尔值、时长、URL、日志参数、请求限制和 Header 限制都会导致启动失败，不再静默回退。

模型网关地址 `AEP_MODEL_GATEWAY_BASE_URL` 会原样通过 `GET /aep/v1/metadata` 下发给全部客户端，必须是集群外客户端可解析、可直达的地址，否则客户端只会在运行期收到莫名失败。启动时即校验：主机为集群内部名称（`*.svc.cluster.local`，或不含 `.` 的单段裸主机名，如 `aep-gateway-authorizer`）在任何环境都拒绝启动；`AEP_ENVIRONMENT=production` 时主机为 localhost 或回环地址（127.0.0.1、::1 等）同样拒绝启动，development/test 放行以便本机联调。未配置该变量不阻止启动，但 metadata 不再公布 `model_gateway` capability、也不输出 `modelGateway` 字段，客户端按能力关闭处理。启动日志会打印生效的模型网关地址，未配置时输出一条 warn。

Mock 联合认证只属于本地开发夹具：它用一个字面量授权码换取 bootstrap 管理员会话。默认关闭；开启时要求 AEP_ADDRESS 绑定 loopback 地址；生产环境带着它启动会被拒绝。在真实企业身份适配器接入前，不得公布或暴露 federated_auth。

部署输入参考 [control-service.env.example](../deploy/production/control-service.env.example) 与 [gateway-authorizer.env.example](../deploy/production/gateway-authorizer.env.example)。敏感变量支持 `VARIABLE_FILE` 文件路径，直接值与 `_FILE` 形式不能同时设置。Credential keyring 继续使用 `AEP_CREDENTIAL_MASTER_KEY_FILE`，以便受控轮换期间保留旧解密密钥。数据面 reconciler token 与网关 License 状态令牌也支持文件形式。Kubernetes 基线通过 External Secret 挂载 License 可信公钥集、签名 License 和两个服务共享的状态令牌；清单中的客户 ID 与部署 ID 必须由交付 overlay 替换为 License 对应值。

`AEP_MODEL_GATEWAY_BASE_URL` 是服务 metadata 向客户端下发的模型网关地址的启动期回落值。管理员可以通过 `PUT /aep/v1/admin/deployment/settings` 在运行时替换它（见 [api-v1.md](api-v1.md)）：校验通过的覆盖值存入 PostgreSQL，在显式置 null 清除之前优先于环境变量值；指向集群内部主机名（或生产环境回环地址）的取值会被拒绝。客户端通过既有的 metadata 轮询自动收敛到新值，无需重启 control-service 或执行 kubectl 滚动更新。

签名 seed、Credential keyring、数据库凭据、对象存储凭据和初始管理员密码必须由编排平台的 Secret Provider 提供，不得写入镜像、ConfigMap、Git、Helm values 或 shell 历史。

## 密码认证

密码必须包含 12 至 1024 个 Unicode 字符，并使用 Argon2id 存储。临时密码会话在完成改密前由服务端限制，所签发的 model token 不包含任何模型作用域。登录失败按不透明的“来源”和“来源 + 主体”键记录到 PostgreSQL，在多个 control-service 副本间共享渐进退避，同时避免单个远端来源把某个主体对其他来源也永久锁死。`AEP_LOGIN_FAILURE_LIMIT` 控制“来源 + 主体”阈值，`AEP_LOGIN_SOURCE_FAILURE_LIMIT` 控制单一来源的累计失败阈值；部署方应结合 `AEP_LOGIN_FAILURE_WINDOW`、`AEP_LOGIN_BACKOFF_BASE` 和 `AEP_LOGIN_BACKOFF_MAX` 按威胁模型调节参数。

服务默认忽略 `X-Forwarded-For`。部署在反向代理后时，只有在代理已经覆盖或清洗客户端传入的转发头后，才能把代理的精确 CIDR 以逗号分隔配置到 `AEP_TRUSTED_PROXY_CIDRS`。服务会从右向左检查转发链，取第一个不属于可信代理范围的地址。不得为了方便直接信任全部内网网段，否则能够从可信网段直连服务的客户端可自行选择限流身份。

认证审计记录包含部署、会话标识及不透明的主体/来源哈希，但不会包含用户名、密码、Token 或请求体。退出登录、管理员重置密码、禁用账号或撤销会话后，管控面会在每次认证请求中检查用户与会话的当前状态，因此受影响的 access token 立即失效。model token 仍由网关本地验签；生产网关的 entitlement 状态检查会在配置的状态缓存窗口内观察到同一会话撤销。

## 数据留存

control-service 默认每 15 分钟（`AEP_RETENTION_CLEANUP_INTERVAL`）执行一次有界 PostgreSQL 清理。事务级 advisory lock 保证多个副本中同一时刻只有一个副本清理，每条根删除语句每轮最多选择 `AEP_RETENTION_CLEANUP_BATCH_SIZE` 行；外键级联可能额外删除其从属 token 或投递行。活动会话、仍可使用的 refresh token、尚未过期的活动控管事件，以及待处理或可重试投递不会被选为清理根。每轮数据库操作最长 30 秒，实际删除数据时会按类别记录直接删除行数。

`AEP_OPERATIONAL_RETENTION` 默认 720 小时，覆盖过期/已撤销的会话令牌、非活动会话、终态控管投递，以及已过期或非活动控管事件。`AEP_TELEMETRY_RETENTION` 默认 2160 小时，覆盖遥测和 Skill 同步结果。`AEP_AUDIT_RETENTION` 默认 8760 小时，覆盖认证、Credential 解析、License 和管理操作审计。登录限流键会在“失败窗口”和“最大退避”两者较长的期限后清理。单独把某个留存窗口设为 `0` 可用于经批准的法务保留；把清理间隔设为 `0` 会完全停用后台任务。两种情况都必须配套独立的数据库容量监控和有记录的手工清理流程。备份仍会保留清理前捕获的数据，因此备份到期策略也必须与获批留存策略一致。

## 运行端点

| 端点 | 语义 | 编排用途 |
| --- | --- | --- |
| `/livez` | 进程仍能提供 HTTP，不检查依赖 | Liveness probe |
| `/readyz` | 管控服务检查 PostgreSQL 与 RustFS；网关检查可信 JWKS 可刷新；reconciler 检查所有已配置 deployment 的最近一次同步均成功 | Readiness probe |
| `/healthz` | `/readyz` 的兼容别名 | 现有集成 |
| `/metrics` | Prometheus/OpenMetrics，包含稳定路由、方法、状态、延迟和并发数 | 内网监控采集 |

指标不会使用企业、用户、Agent、资源 ID、请求 ID、查询串或 Token 作为 label。访问日志只记录请求 ID、方法、稳定路由、状态、响应字节数与耗时。生产默认 JSON 日志，不记录 Authorization、请求体、查询串、Credential 明文或模型 Prompt。

三个 distroless 镜像均提供内置探针命令：

```sh
/aep-control healthcheck http://127.0.0.1:8080/readyz
/aep-gateway-authorizer healthcheck http://127.0.0.1:8090/readyz
/aep-gateway-reconciler healthcheck http://127.0.0.1:8091/readyz
```

reconciler 不提供旧版 `/healthz` 别名。它的 `/livez` 不受 control-plane 或 Kubernetes 可用性影响；`/readyz` 启动时为未就绪，并持续反映每个已配置 deployment 最近一次同步的结果。

## 可用性与发布

数据库 migration、初始管理员初始化和数据留存清理都使用 PostgreSQL advisory lock 串行执行；多个 control-service 副本可同时连接新库或升级库，不会竞争 schema 与 bootstrap 写入，同一轮清理也只由一个副本执行。RustFS bucket 首次并发创建同样可安全收敛。

当依赖服务达到相同可用性目标时，control-service 与 gateway-authorizer 应至少各部署两个跨故障域副本。发布顺序：

1. 备份 PostgreSQL、Skill bucket、Ed25519 签名 seed 和完整 Credential keyring。
2. 启动一个新 control-service 副本并等待 `/readyz`。
3. 滚动更新其余 control-service，再更新 gateway-authorizer。
4. 检查错误率与延迟指标，并验证 Agent 登录、Credential 解析、Skill 下载和模型调用。
5. 观察窗口结束前保留上一版本镜像 digest。

数据库 migration 只向前执行。仅当旧二进制兼容新 schema 时才能直接回滚应用；否则必须恢复同一恢复点的 PostgreSQL 与 RustFS，并恢复匹配的签名 seed 和 Credential keyring。

## 模型路由异步发布入队（默认关闭）

`AEP_GATEWAY_PUBLICATIONS_ENABLED` 默认 `false`，提供快照、operation、审计与 outbox
的事务入队。`AEP_GATEWAY_PUBLICATION_RELAY_ENABLED` 独立默认关闭；同时开启后，
Control Service 启动集群内 relay，把当前 `AEP_DEPLOYMENT_ID` 的意图投影为
`aep-system` 内的 ModelRoute／ModelPolicy 并更新预先配置的单例 DeploymentConfig。命名空间当前
固定为 `aep-system`；其他值在开启时拒绝启动。不读取 kubeconfig，不读取 Secret，
不写 Higress 资源或 status。初始化集群凭据失败会阻止启动。

发布请求选择 `includeGatewayLimits: true` 时，在同一事务捕获模型目录与限流草稿，
包含禁用和软删除规则。省略或 false 继承最新已入队的策略意图，不隐式发布新草稿。
部署级规则使用一个 DeploymentConfig 目标，不按模型拆分共享计数器；指定模型规则
使用 ModelRoute 目标。保留原生规则 ID／版本及禁用墓碑，重试不能复活旧规则。
先安装新增 ModelPolicy schema、operator 与 relay 的窄 RBAC，再启用组合发布。
限额主体仅限定计数范围，不授予用户、团队或角色模型访问权限。

RBAC 见未纳入默认 kustomization 的 [显式授权清单](../deploy/kubernetes/production/publication-relay-rbac.yaml)。
部署前必须安装匹配 CRD、配置 singleton/native gateway、确认旧 reconciler 停写并完成
operator 独占写移交，网络策略须允许 Control Service 到 API Server。不要直接用于已有
无归属 CR 的命名空间：relay 会拒绝接管。默认配置、Console 与现有期望状态路径不变。
默认 Control Service ServiceAccount 关闭自动 token 挂载；显式部署覆盖还需给该 Pod
投射会轮转的短期 ServiceAccount token、`kube-root-ca.crt` 与 namespace，挂载到标准
`/var/run/secrets/kubernetes.io/serviceaccount` 路径。不能把管理员 kubeconfig 或静态
长效 token 写入环境变量。上述 RBAC 清单不会自动开启 token 挂载或 relay 开关。

claim 30 秒到期、每轮 Kubernetes 操作总预算 10 秒；进程崩溃后其他副本重新获取
token，核对 CR 后继续。临时失败按 client-go 原生退避并持久化下次时间；多个副本
共享 DB claim。新快照取代旧待投递任务，每个 CR 的序号和 resourceVersion 拦截旧写入。
多个 CR 逐个最终收敛，不是原子快照切换；operator 可能观察到部分投递的中间状态。
历史模型保留禁用墓碑（含从未成功投递的旧快照），本阶段不 GC，不允许其他 spec 写者
或删除墓碑，否则失去晚到创建防护。墓碑容量和后续安全回收须纳入容量规划。

projected 仅确认 AEP CR 投递；最新 projected 任务可只读回查 operator 的 Accepted／
Applied。须先升级生成 CRD 与 operator，使 status 带准确的 observedPublication，
并核对发布标识、spec、generation 与聚合／路由应用摘要。旧版本、混合或不可用证据
保持 Unknown，新 queued 任务也使旧证据失效。读取不领取任务、不改变 phase、不读
Secret、不写 K8s，沿用 relay 既有 get／list 权限。观测不额外持久化；Ready 始终为
Unknown（DataPlaneNotObserved），不代表 Higress 已加载。仅用于隔离验收，生产切换
仍需独立数据面验证。回滚旧 operator 后证据安全退回 Unknown；保留新增 CRD 字段，
不要借回滚删除 status schema、墓碑或发布序号。

新增 `data_plane_publication_snapshots`、`data_plane_publications` 和
`data_plane_publication_outbox` 表纳入 PostgreSQL 备份。迁移只新增表，旧二进制仍可
运行；回滚应用不得删表或丢弃已受理任务。当前不自动清理这些表，最短幂等留存为 24
小时，待投递意图不会因到期被删。迁移 047 增加 claim、重试与完成字段，同样纳入备份。
回滚前先关闭 relay 并排干进程；保留 CR、墓碑和任务，禁止开启第二个 Higress 写入者。
恢复 DB 早于 CR 的备份时，遇到更高 CR 序号会停止旧投递，不能清零标记强行回退。

参见 [异步发布 API](api-v1.zh-CN.md#operator-异步发布入队阶段)。

## 备份恢复

使用维护窗口或协调存储快照，使 PostgreSQL 与 Skill bucket 属于同一恢复点。PostgreSQL 保存身份、授权、Credential 密文、事件、审计和对象引用；RustFS 保存不可变 Skill ZIP。签名 seed 和所有 Credential keyring 条目必须由 Secret 系统独立备份，旧密钥丢失会导致对应 Credential 无法解密。

Compose 操作工具、私有文件权限、可选 AES-256-GCM 信封加密及隔离恢复流程见 [backup-restore-runbook.zh-CN.md](backup-restore-runbook.zh-CN.md)。备份密钥加密密钥必须与备份目录及仓库分开保管。

先恢复到隔离的 PostgreSQL 与 RustFS，校验对象数量和数据库完整性，再只启动一个 control-service 运行内嵌 migration。验证 `/readyz`、JWKS 连续性、Credential 解析审计和 Skill checksum 后，才能增加副本或切换流量。

## 验证

```sh
npm run test:e2e:runtime
npm run test:e2e:upgrade
```

运行基线场景验证首次并发启动、依赖感知 readiness、独立 liveness、Prometheus 指标、结构化日志、有界数据留存清理、容器权限收敛和 SIGTERM 零退出。升级场景从迁移 009 的旧 schema 启动，验证 forward-only migration、旧数据转换与保留、多个副本并发迁移、服务重启和升级后的 API 可用性。完整发布门仍为 `npm run test:e2e`。

Kubernetes、Higress Helm、TLS、RBAC、External Secrets 与在线数据面收敛基线见 [production-data-plane.zh-CN.md](production-data-plane.zh-CN.md)。

# 网关凭据直接交付

独立的凭据发布组件补齐短链路，无须为模型凭据额外部署 Vault 或 ESO：

`Admin Console → AEP 加密 PostgreSQL Credential → 发布组件 → Kubernetes Secret → 网关 Operator → Higress`

HTTP 管控服务仍无 Secret 写权限；Operator 仍无业务 DB／keyring 访问权限。
发布组件复用现有 AES-GCM／keyring，只负责交付 Secret，不发布路由、不渲染 Higress。
CR、响应和日志不携带供应商明文；既有渲染器仍会将 Secret 值内联到 Higress
插件配置，因此渲染产物也需要严格保护。

## 同步范围与生命周期

- 仅交付当前部署中，至少被一个已启用 `gateway` 模型引用的已启用 `server_only`
  API Key；模型协议为 `openai-compatible` 或 `anthropic`。Agent 下发凭据不进入集群。
- Secret 名称为 `aep-credentials/aep-credential-<credentialId>`，键为 `api-key`。
  新增模型绑定触发后续对账创建，轮换更新同名 Secret。
- 凭据停用／删除、改为 Agent 下发、最后一个有效模型解除引用或停用时，清理自己
  拥有的 Secret；共享凭据仍被其他有效模型引用时保留。
- 校验管理标签、部署及凭据身份、ownerReferences 和删除状态；拒绝接管 ESO、其他
  部署或手工创建的同名 Secret。更新使用 resourceVersion，删除使用 UID＋版本前置条件。
- DB 查询错误不等于不存在，不能据此删除。解密／集群失败使同步未就绪并持续重试；
  一个凭据失败不阻止其他凭据轮换或撤回。重启与下一轮对账能修复遗漏及漂移。

这是**周期对账的最终一致性**，不是 HTTP 内同步写集群或事务 outbox。每轮完成后
默认等待 5 秒，每页最多 100 条，操作超时 10 秒；数据量、故障和冲突会延长交付时间。
API 成功仍只表示“已保存 DB”；Secret 交付、路由发布、Operator Applied 和数据面实际
生效是不同检查点。撤回和轮换还需等待 Operator／旧 reconciler 更新下游配置。

使用 controller-runtime 标准选主生命周期。默认一个副本，滚动更新不并行启动新旧
副本；停机期间已有 Secret 保留。`/readyz` 只在完整同步成功后就绪，`/healthz` 不受
DB 或 Kubernetes 故障影响，端口为 8095。

## 部署与安全边界

1. 先启动正常 AEP／网关，确认 `aep-system`、`aep-credentials` 存在；该进程不迁移 DB
   或初始化账号。
2. 使用部署方的受保护 Secret 流程准备 `aep-system/aep-credential-publisher-config`，
   仅包含 `database-url`、`credential-keyring`。数据库账号应独立，仅授予连接、schema
   USAGE，以及 `credentials`／`models` SELECT。keyring 与管控服务一致且保留历史键。
   不挂载管控服务整套 Secret，不将私密值放进镜像、Git 或命令历史。
3. 修改部署 ID，并执行 `kubectl kustomize deploy/kubernetes/credential-publisher` 评审。
4. 用 `KO_DOCKER_REPO=<registry> ko build ./services/control-service/cmd/credential-publisher`
   构建并固定镜像 digest，或使用 ko apply。基镜像需非 root 且包含 CA，核对入口与 UID。

发布组件持有解密能力，是受信任的密钥交付组件。应用层部署过滤不等于 DB 行级隔离。
RBAC 仅允许在 `aep-credentials` 读、列举、创建、更新、删除 Secret，并在 `aep-system`
创建 Lease／读写固定名称 Lease；没有 Higress 写权限或管控服务 Secret 读取权限。
Secret 权限覆盖该命名空间，标签／归属检查只是应用防护，不是逐对象 RBAC。

配置见[英文完整说明](credential-publisher.md#install-and-privileges)：必须设置
`AEP_CREDENTIAL_PUBLISHER_DEPLOYMENT_ID`、数据库 URL 文件和
`AEP_CREDENTIAL_MASTER_KEY_FILE`；间隔由 `AEP_CREDENTIAL_PUBLISHER_INTERVAL` 调整。
除显式 development／test 外要求 DB 使用验证证书的 TLS，禁止回落明文连接。
网络清单仅放行 DNS、API 443、DB 5432；按环境 overlay 收敛地址并调整端口。

## ESO 兼容、迁移与回滚

ESO 仍可为已有企业密钥库的部署供给凭据，也可继续供给 AEP 的启动 Secret；本组件
不会卸载 ESO／Vault，也不会修改现有 ExternalSecret。

同一个供应商 Secret 只能有一个所有者。迁移前停掉对应外部供给并评审目标 Secret
的归属转换；`creationPolicy: Owner` 的 ExternalSecret 删除时会级联删除 Secret，
必须评估重新创建前的窗口。组件不会偷偷接管旧 Secret，先在测试环境演练。

停止／卸载组件会保留已交付 Secret，避免回滚时统一撤销密钥。切回 ESO 前也需显式
迁移其目标，避免并发覆盖。恢复依据仍是 DB Credential 与匹配的 keyring。

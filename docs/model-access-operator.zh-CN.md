# 模型访问原生投影

简体中文 | [English](model-access-operator.md)

这是 [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338) 的显式启用阶段：
在既有 Kubebuilder Operator 中添加真实控制器，消费已准入的
[ModelAccessGrant](model-access-grants.zh-CN.md) 会话绑定，生成自有的 Authorino v0.26.4
AuthConfig 与新鲜度 ConfigMap。本阶段不替换默认 authorizer，不接通共享 Higress 入口，
不代表完整业务验收；员工工作负载另有[显式启用的原生验证阶段](workload-native-access.zh-CN.md)，
正式采用门禁仍未通过。

## 所有权与配置

DB 授权仍是事实源，继续使用已有事务 outbox 与 publication relay。此控制器没有自己的
DB 或 Secret 访问，不增加第二条发布链路。Grant 必须准确匹配绑定摘要名称、relay 标记、
部署标签及当前 DeploymentConfig。支持人类与 agent 的 **session** 绑定；Kubernetes
workload 绑定默认仍拒绝；独立的[原生工作负载验证阶段](workload-native-access.zh-CN.md)
增加显式 audience 与 TokenReview RBAC，未启用时保持原行为。

默认部署和默认 RBAC 不变。显式渲染
`kubectl kustomize services/gateway-operator/config/access-rbac` 只生成额外的
`aep-system` Role 与 RoleBinding，绑定既有 Operator 身份：读取 grant/部署配置、更新
grant status、创建/更新/监听 ConfigMap 与 AuthConfig。不增加 Secret、TokenReview、
全局绑定或源资源删除权限。已有控制器保留各自原有权限。仍须部署侧保证独占写权限；
field manager 与 owner reference 不能阻止另一个高权限写入者。

开启时必须配置：

| 变量 | 含义 |
| --- | --- |
| `AEP_GATEWAY_ACCESS_OPERATOR_ENABLED=true` | 显式启用，否则不注册新控制器 |
| `AEP_GATEWAY_ACCESS_ISSUER_URL` | 可信 AEP JWT discovery issuer |
| `AEP_GATEWAY_ACCESS_POLICY_BASE_URL` | 提供投影 Rego 文件的可信静态服务 |
| `AEP_GATEWAY_ACCESS_REFRESH_SECONDS` | 显式设置原生刷新间隔 1–60 秒；不是生产默认参数 |
| `AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT` | 默认 true；false 时额外接受 model JWT |

非法布尔值、时间间隔与 URL 均拒绝启动。URL 只允许 HTTP(S)，禁止 userinfo、query、
fragment。HTTP 允许项用于隔离测试，不是生产传输安全方案。策略源可以改变授权边界，
必须限制写入者与网络调用者，审查 TLS/NetworkPolicy 部署。本阶段没有交付这项生产静态
服务或 overlay；独立评审的 [HTTPS 文件源 #357](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/357)
提供此组件。Node 文件源只是一次性测试夹具，不得作为产品部署。

## 稳定语义与独立续期

### 员工身份存储前置阶段

迁移 055 新增内部、无凭据的绑定登记：明确关联 AEP agent ID、员工 namespace/name/UID、
专属 ServiceAccount name/UID 与网关 audience。它不是公开注册 API，也不授予模型权限。
可信生命周期写入者须读取当前员工及其控制器拥有的 SA，核对双方 UID 和明确的 agent
映射后调用 repository；调用方 header 或人类 owner 均不构成身份依据。

登记须携带预期的当前 binding key。替换 SA 与退役旧绑定在同一事务完成，旧观察不能覆盖
新身份。历史 SA 身份不能转移或复活；账号删除会退役存活观察并保留历史。每个 agent 最多一个当前绑定。
该员工的多个副本使用其专属 SA，并非每个对话单独登记身份。可信观察的新鲜度限于
2 秒至 1 小时，使用 DB 时钟；这是准入范围，不是生产传播时限建议。过期或退役观察不会
进入业务源的实时 `workloads`。过期但仍为当前身份的记录单独放在
`unverifiedWorkloads` 中，用于临时拒绝；已退役身份只保留在不可变历史中。

绑定变更、退役、新鲜度缩短和过期后恢复会在同一事务触发既有的定向源信号；连续延长观察
期限不会使授权失效。模型与成员关系仍来自既有有效角色，观察期限参与自然到期计算。

可选可信 watcher 由 [#365](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/365)
单独交付。身份登记不签发或读取令牌，不配置 TokenReview，也不让运行时消费令牌文件。
原生工作负载验证仍须单独启用，默认 entitlement 要求仍拒绝不具备许可证实现的工作负载
渲染。不能仅凭登记表启用员工模型访问。

### 会话/工作负载原子发布

配置既有 `AEP_GATEWAY_ACCESS_RELAY_ENABLED` opt-in 后，原 access worker 改用
`CaptureModelAccessBindings`：在既有配置锁和同一 MVCC 业务快照下，推导当前及历史
会话、可信工作负载的全部流，在同一事务写入不可变快照/outbox 并精确确认来源版本。
任一绑定或确认失败均整体回滚，事务内没有网络操作，不增加 writer 或公开 API。
旧的窄接口 `CaptureModelAccessSessions` 继续拒绝不完整的工作负载覆盖。

员工身份仍为已核实的 agent/CR/SA UID 组合；模型、角色、团队和许可证引用复用人类会话
的角色派生业务源，不把 owner 或对话身份当成员工。未知/畸形绑定、历史直接授权和已退役
身份重放仍失败关闭。移除/替换 SA 或删除员工时，在同一捕获中为旧流发布不可复活的
终止记录，并包含当前替换身份（如有）。

观察到期但登记仍为当前身份时，发布 disabled、**非 retired** 状态，新的可信证据仍可
恢复相同身份，未核实时不会放行模型。允许期限同时受观察、账号、模型、许可证与显式
发布新鲜度约束，整秒向下截断。持久定时任务在到期前重新捕获健康观察续期，不把每次
续期当作权限变更，语义 hash 保持稳定；最后不足两秒时等待到期，避免忙循环。无关主体
不会重发。这不证明运行时缓存必然存续，也不证明故障情况下服务副本会及时采用。

原生工作负载许可证边界、运行时配置交付及完整员工模型/配额/撤权验收仍由
[#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338) 与
[governance #131](https://github.com/rongxinzy/aep-deerflow-governance/issues/131) 分阶段跟踪。
`adoptionGate=false` 保持，默认部署和推理入口不变。

既有管理侧 M2M 令牌接口签发指定用户的模型 JWT，不证明员工的 Kubernetes 身份。
本登记不改变该接口，也不把调用方提供的对话 ID 当作可信的工作负载绑定。

### 会话投影

每个会话绑定拥有一个稳定 AuthConfig 与一个 ConfigMap。Operator 根据实际语义及可信
部署选项独立计算 hash，不直接信任源 publication 的 contentHash。角色、团队、模型、
许可证、配置变化会改变语义；签发时间、有效期和 publication 标记不改变语义。
模型、角色、团队和许可证排序后参与计算。

原生认证缓存已验证的 AEP JWT 身份，原生 OPA 缓存完整会话决策及允许模型范围。另一个
**不缓存结果**的原生 OPA 边界每次请求校验语义 hash、实际 JSON `body.model`、Token
scope、数字时间字段、发布绝对期限、精确许可证 ID/digest 及宽限期限。热结果仍执行
这些边界检查；没有逐请求业务 DB 查询，也没有自研授权缓存。本阶段仅支持 JSON body
模型请求，不支持多模态链路中的 multipart 和从路径读取模型的请求。

续期只写 ConfigMap，不能改写 AuthConfig 的 metadata/spec/status，因为上游元数据事件
也可能重建 evaluator 缓存。先写 ConfigMap 再写 AuthConfig，混合语义版本通过 hash
错配拒绝。Sequence/owner 护栏、实时父资源检查、resourceVersion 冲突及读回限制重放与
外来资源接管。Kubernetes 没有跨对象事务：检查与子资源写入之间仍可能发生父资源变更。
Watch 重试与最后发布的绝对期限约束收敛，不能声称实现了原子撤权。

Authorino 按 evaluator/进程轮询刷新外部模块，不直接 watch ConfigMap；下载/编译失败
保留最后良好模块，其绝对期限到期后拒绝。ConfigMap volume 投影还存在独立传播延迟。
删除 grant 使用同命名空间 owner GC；GC 与策略源故障不能延长最后已编译的期限。

`Applied=True` 仅表示原生资源读回一致。`Ready` 始终 false，清空 replicas 观察记录：
API 写入、Pod 就绪、原生 AuthConfig status 与 Lease 选主均不证明所有服务进程已采用。
内部 `x-aep-access-observation` 响应只包含语义 hash/publication，不是缓存命中见证、
可信主体头或对外 API；后续入口集成必须清洗调用方身份提示，并将该证据保留在内部。

## 验证与剩余门禁

单测/API 测试覆盖所有权、旧源写入、同版本内容篡改、多余 binary data、到期、status
隔离、配置校验和可选命名空间权限。配置 `KUBEBUILDER_ASSETS` 后，CI 使用中性原生存储
schema 运行真实 API Server 测试。额外配置 `AEP_AUTHORINO_CRD_PATH` 指向固定 v0.26.4
CRD，会检查 SHA-256 并验证真实准入默认值；中性 schema 不能作为原生准入证据。

显式 probe 用 ko 构建当前 Operator，在独立临时 Kind 中运行两个 Operator 和两个固定
Authorino 副本，使用真实 AEP 签发、discovery/JWKS 输出，验证允许/拒绝、错误 JWT、实际
body 模型越权、不改 AuthConfig 的续期及禁用撤权。安装的是 Higress schema 夹具，
**不是**真实 Higress 网关、业务 DB、供应商或员工运行时。Kind 仅是本地测试工具。

```powershell
$env:KIND = 'path/to/kind'
$env:KO = 'path/to/ko'
$env:AEP_AUTHORINO_CRD_PATH = 'path/to/pinned/authconfigs.yaml'
$env:AEP_NATIVE_ACCESS_REPORT = 'path/outside/repository/report.json'
$env:AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT = 'true'
node tests/e2e/native-access-projection.mjs
```

Docker 需已缓存 `node:24-alpine`、固定 Authorino v0.26.4 tag 和脚本指定的固定 Kind
节点镜像。导入 Authorino 时保留原始 OCI index/digest；单平台导出会改写 index，可能
留下 containerd 悬空 import 引用。报告不保存凭据。

与 #357 实际双副本 HTTPS 清单联验时，额外将 `AEP_NATIVE_ACCESS_POLICY_SOURCE_DIR`
指向其 `deploy/kubernetes/access-policy-source` 目录，并设置 `OPENSSL` 可执行文件。
测试在仓库外创建临时 PKI，将临时 CA 追加到固定 Authorino 镜像的完整公共信任包。
两个授权进程消费文件源；第三个冷启动负向对照不信任该 CA，必须因 x509 校验拒绝。
进一步检查源中断、绝对到期、离线续期不能延长旧许可、恢复后采用新版本，且不改写
AuthConfig。这仍是 CR 起点的组件联验，不是业务 DB/Higress/员工或 NetworkPolicy
执行验收。临时私钥文件及自有集群均在 `finally` 清理。

### 从真实管理 API 开始的业务模式

在上述 HTTPS 参数基础上，增加以下配置后运行同一入口：

```powershell
npm run build --workspace @aep/sdk-node
$env:AEP_NATIVE_ACCESS_BUSINESS = 'true'
$env:AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT = 'false'
node tests/e2e/native-access-projection.mjs
```

Docker 还需缓存 `postgres:17.6-alpine` 与 `rustfs/rustfs:1.0.0`。脚本用 ko 构建当前
Control Service，在自有 Kind 内部署一次性 PostgreSQL、对象存储和真实服务。随机
管理员/存储密码、签名 seed 和 Credential key 只作为运行时 Secret 注入，不进入镜像、
仓库或报告。整个夹具保留 restricted Pod Security；不访问现有本地或生产数据库。

业务写入只通过 SDK/公开管理 API：模型仅赋予角色，用户通过直接角色或团队默认角色
获取模型。验证团队默认角色、用户直接角色及团队成员变化，角色/团队停用与恢复、
角色模型授权删除与重建、单会话撤销、账号停用/重新启用、模型禁用/恢复、
真实 Control/relay 中断后的到期拒绝和恢复，以及一个鉴权进程重建后的重新发现。
只读 SQL 将最新 DB publication 的操作 ID、序号、摘要和阶段与 CR 及执行观察关联；
不通过 SQL 或手改 CR 模拟授权变更。

验收观察器独立枚举当前两个 Ready Pod，逐 Pod 直连采集 UID、containerID 和重启
次数，并在请求前后核对实例集合。此观察器只属于测试，既不是产品级全进程 observer，
也不写 `Ready=True`；内部执行响应同样不是缓存命中见证。业务模式使用真实服务签发的
人类会话 model JWT，仍不覆盖企业 entitlement、可信员工 workload、共享 Higress
入口、供应商推理、配额或 NetworkPolicy 执行。`adoptionGate` 继续为 false。

### 可选真实 Higress 共享入口验收

在业务模式和 HTTPS 参数基础上设置 `AEP_NATIVE_ACCESS_HIGRESS=true`，并配置
`HELM`。可通过 `AEP_HIGRESS_CORE_CHART` 指向本地 Higress core **2.2.4** chart；
不设置时从官方仓库获取该固定版本。Docker 需缓存同版本 `gateway`、`higress`、
`pilot` 镜像，完整地址见 `tests/e2e/auth-cache/higress-install.mjs`。此模式安装真实
Higress CRD 和控制器，不再安装 schema 夹具。

共享入口使用原生 Envoy gRPC `ext_authz` 和逐绑定路由的 `context_extensions.host`，
将测试专用、非可信的 `x-aep-access-binding` 提示映射为鉴权配置。Authorino 必须继续验证实际 JWT
中的主体、会话及实际请求体模型；提示本身不授予权限，也不是已发布的公共契约。
上游为不包含凭据的 echo，只返回敏感头存在性，不反射令牌或请求正文。原始推理
Host/路径保留，bearer、提示及测试列出的身份/Consumer/内部观察头在转发前移除。

测试覆盖跨会话/空/未知提示、错误 bearer、伪造身份、实际模型越权、异常或超大正文，
并以独立上游计数器确认拒绝请求未到上游。业务模式中的角色/团队/模型变更、会话
撤销、Control/relay 中断到期和恢复也通过真实 Higress 检查；另外停止全部鉴权进程，
确认网关拒绝转发并在恢复后正常放行。原有逐 Pod 检查仍保留。

实验先尝试了 HTTP `ext_authz` formatter 改写 Host/路径；真实网关以
`:-prefixed or host headers may not be modified` 拒绝监听器配置。不能将 formatter
存在视为该用途可用。当前 gRPC 方案由测试为实际会话建立显式路由；产品级独占 writer、
路由动态发布/回收、全副本采用和容量成本尚待实现与验证。
测试还明确检查精确头列表的边界：未列出的 `x-aep-*` 头（包括动态限流头）仍会到达
echo。此项按已知限制记录，不是安全能力通过；正式接入必须清理整个保留头命名空间，
再由可信鉴权结果生成配额身份，不能直接采用该测试夹具。

这是隔离验收配置，不是生产默认入口：不验证供应商推理、企业 entitlement、员工
workload、可信 Consumer/Redis 配额身份、NetworkPolicy 或缓存命中；没有新增可信
身份头发布能力。完整产品采用仍需补齐这些门槛，`adoptionGate=false`。

正式采用前还需完成：

- 更广业务场景的 DB→outbox→relay→CR→Operator 覆盖，包括许可证、计划到期与 agent
  绑定；产品级全服务进程发现/采用确认，以及安全共享 Higress 入口。上述人类会话
  probe 不能替代这些门禁。
- 可信员工 workload 绑定，兼容原有 Consumer/Redis 配额身份与多模态请求模型提取；
  本阶段不增加配额记账。
- 安全动态静态文件交付、watch 中断、到期恢复及部署配置变更。软件/选项升级可能在同
  source sequence 下改变渲染文件，会被护栏有意拒绝：停止旧 writer，取得新的可信
  publication，再启用改变后的渲染语义。不得混用不同配置的副本或伪改旧标记。
- 容量、并发与驱逐。原生缓存按每绑定每 evaluator 分配内存；freecache 拒绝超过其
  容量 1/1024 的单条 entry。实测企业 JWT 原始 key 加身份内容超过上游默认 1 MB 的
  单条限制。本 probe 的 4 MB 仅覆盖小型夹具，不证明最大 scopes 或部署规模可用。
  一年 TTL 只是保留上限，Token/发布/许可证期限仍独立检查；存储保留与高基数成本
  必须实测评审。

[组件续期实验](auth-result-cache-feasibility.zh-CN.md) 独立证明原生机制，不能替代上述产品
门禁。保持 `adoptionGate=false`，继续使用既有默认 authorizer。

# 员工工作负载的原生身份验证

简体中文 | [English](workload-native-access.md)

这是 [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338) 与
[员工接入计划](https://github.com/rongxinzy/aep-deerflow-governance/issues/131) 的独立网关阶段。
既有 Kubebuilder Operator 可将 `ModelAccessGrant.binding.kind=kubernetes` 渲染为
Authorino v0.26.4 原生 TokenReview 与 OPA 配置。默认关闭，不切换现有模型入口。

## 信任与配置

DB 中的角色授权仍是事实源。一个员工对应其 AEP `kind=agent` 主体和专属 SA；不能使用
开启对话的用户或 owner 代替员工身份。可信发布者必须验证员工 CR UID、SA 名称及 UID
的实际关联，再使用既有 outbox/relay 发布 Grant。下面的可选生命周期观察器负责记录该
证据，不发布 Grant。会话/工作负载混合发布、带企业许可证的员工推理仍是未完成的独立
阶段；DeerFlow 可选 token-file 消费见 [deer-flow#34](https://github.com/rongxinzy/deer-flow/pull/34)。

### 可选可信生命周期观察器

`services/control-service/cmd/workload-observer` 是单独启用的进程，不是公开 HTTP 注册
接口，也不在默认镜像/overlay 中。它依赖 [governance#149](https://github.com/rongxinzy/aep-deerflow-governance/pull/149)
的无凭据 `status.modelIdentity` 契约，以及由正常 control-service 迁移流程安装的既有
工作负载登记表；观察器自己不运行迁移。

controller-runtime Manager 监听一个指定命名空间内的 DigitalEmployee 和 ServiceAccount。
事件只调度工作队列；注册和续期必须用实时 APIReader 核对显式 `spec.modelAccess.agentId`、
`spec.governance.deploymentID`、员工 CR UID、当前 observedGeneration、audience、
专属 SA 的精确名称/UID、controller owner 和关闭自动挂载令牌。读取 SA 后再次读取员工，
拒绝期间变化的对象。不会把 `defaultAEPUser` 或对话 owner 当作员工主体。

既有 DB 登记事务继续验证 agent 类型、不可复活的身份历史及 CAS 替换。健康证据按新鲜度
的一半续期；状态落后或 API 暂时失败只暂停续期，不永久注销仍存在的身份；当前登记即使
已过期，也可由新的实时正向证据安全续期。确认员工/SA 删除、显式移除绑定或改为其他部署
时，只注销精确的已观察 key。停机期间漏掉的删除仍由观察到期限制；Kubernetes 与 DB 并非
原子事务，也不承诺删除 Pod 即刻撤权。

先渲染可选清单，再通过交付 overlay 调整并显式安装：

```bash
kubectl kustomize deploy/kubernetes/workload-observer
```

| 设置 | 要求 |
| --- | --- |
| `AEP_ENVIRONMENT` | 显式 `production`、`development` 或 `test` |
| `AEP_WORKLOAD_DATABASE_URL_FILE` | 从 `aep-workload-observer-config` 的 `database-url` 挂载 DSN；与 `AEP_WORKLOAD_DATABASE_URL` 互斥 |
| `AEP_WORKLOAD_DEPLOYMENT_ID` | 与 AEP 部署及员工 governance 绑定精确一致 |
| `AEP_WORKLOAD_NAMESPACE` | 单个命名空间，与 Role/RoleBinding 一致 |
| `AEP_WORKLOAD_AUDIENCE` | 与投影令牌及 Grant 的 audience 精确一致 |
| `AEP_WORKLOAD_FRESHNESS` | 显式整秒，范围 `2s` 至 `1h`；示例 `1m` |

生产 DSN 必须验证 TLS，不能回退到不安全连接。交付方需提供访问登记表及其既有来源事件
触发器所需的独立 DB 身份；示例不授予数据库权限。命名空间、部署和 audience 均需显式
配置，不自动发现。先安装两个所需命名空间与 governance CRD，再按仓库 ko 流程解析
`ko://` 镜像并固定 digest，按目标 CNI 收紧 API/DB 出站目的地址。观察器没有供应商
密钥、keyring、Secret 读写或 TokenReview 权限；只读取/更新固定名称的
`aep-workload-observer` Lease，使用原生丢租取消和有界 Reconcile context。

8096 端口的 `/healthz` 仅检查进程存活；`/readyz` 在有界 context 内检查 DB 可达与
informer 初始同步。两者都不能证明登记新鲜度、授权、发布或服务副本采用。
停止观察器会暂停续期；不得通过删除登记历史强行回滚。本阶段不新增
schema、公开 API 或 SDK，不切换生产链路，`adoptionGate=false` 继续保留。

显式配置既有 [原生投影](model-access-operator.zh-CN.md)，并设置：

| 设置 | 行为 |
| --- | --- |
| `AEP_GATEWAY_ACCESS_WORKLOAD_AUDIENCE` | 非空可信 audience；必须与 Grant 及投影令牌一致，不能有空白/控制字符，最多 256 字节 |
| `AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT` | 默认 true；本阶段工作负载遇到 true 会拒绝渲染，不以 SA 替代企业许可证凭据 |
| `deploy/kubernetes/access-workload-review` | 单独启用的 Authorino TokenReview RBAC，默认 overlay 不引用 |

该 RBAC 只给 `aep-system/authorino` 对 `authentication.k8s.io/tokenreviews` 的 `create`
权限；它不部署 Authorino、不创建其 SA，也不给 Operator 或员工 Secret、TokenReview、
SubjectAccessReview 权限。部署身份不同须由部署层显式修改绑定。TokenReview 是集群级
API，不能用 namespaced Role 替代。验证服务必须信任签发员工令牌的 API Server；此阶段
不提供跨集群身份联合。

## 请求路径与缓存边界

1. 员工发送 Pod-bound SA bearer。原生 TokenReview 验证签名、audience 与 Kubernetes
   身份；OPA 要求返回的完整 SA username、UID 和 audience 与 Grant 精确一致。
2. 完整 bearer 作为原生身份及决策缓存键；不同员工、轮换令牌和不同副本各有冷缓存。
   不逐请求查询业务 DB，不自研签名验证器或授权缓存。
3. 不缓存结果的原生 OPA 从**已完成 TokenReview 的同一个 bearer**解析 JWT，检查
   数字 `exp/nbf/iat`、sub、audience、namespace、SA 名称/UID 和非空 Pod UID，并检查
   实际 JSON `body.model`、语义 hash 与发布绝对期限。`io.jwt.decode` 只解析声明，不能
   独立证明签名有效；不得删除前置 TokenReview。
4. 角色、模型或绑定变化更新对应 Grant 并重建该语义配置；纯发布续期只更新新鲜度文件。
   沿用原有 Consumer、角色/团队及配额身份，不用 Pod 名称或 owner 作为配额主体。

TokenReview 成功缓存中没有 token 的 `exp`，所以必须执行步骤 3。它也不会主动监听
Pod/SA 删除：热缓存的 Kubernetes 撤销状态可能滞后，**必须**由可信生命周期发布者对
删除/替换发布停用 tombstone，并保留发布绝对期限。此阶段不能承诺删除 Pod 即刻撤权，
也不能承诺没有传播窗口。SA 名称复用不能绕过 UID 比对；员工 CR UID 由可信发布关联
保证，SA JWT 本身不证明员工 CR UID。没有提供 mTLS、非 JWT/非 Pod-bound token 支持。

## 隔离验证

复用已有 Kind 夹具，额外选择工作负载模式：

```bash
AEP_NATIVE_ACCESS_WORKLOAD=true \
AEP_NATIVE_ACCESS_WORKLOAD_EXPIRY=true \
AEP_AUTHORINO_CRD_PATH=/path/to/pinned/authorino.kuadrant.io_authconfigs.yaml \
node tests/e2e/native-access-projection.mjs
```

工具、固定镜像及 Kind 要求沿用[原生投影验收](model-access-operator.zh-CN.md)。测试创建并
清理自己命名的集群，不改现有开发集群。双独立 Authorino 进程验证真实 Pod-bound
令牌、精确身份隔离、模型限制、发布续期/到期、令牌轮换和停用。API Server 指标检查
重复热请求不新增 TokenReview。开启 expiry 模式会等待真实 Kubernetes 十分钟令牌过期，
不改时钟、不伪造签名，并验证热结果在不发起新 TokenReview 时拒绝过期令牌。

这是原生验证器/Operator 的隔离验收，不是完整业务验收：尚未覆盖员工生命周期写入、
业务角色变更到该 Grant 的发布、DeerFlow 实际模型请求、企业许可证、Higress provider
推理或 Redis 配额扣减。`Ready` 不伪造全副本采用结果，正式 `adoptionGate` 仍为 false。

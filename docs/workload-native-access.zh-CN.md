# 员工工作负载的原生身份验证

简体中文 | [English](workload-native-access.md)

这是 [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338) 与
[员工接入计划](https://github.com/rongxinzy/aep-deerflow-governance/issues/131) 的独立网关阶段。
既有 Kubebuilder Operator 可将 `ModelAccessGrant.binding.kind=kubernetes` 渲染为
Authorino v0.26.4 原生 TokenReview 与 OPA 配置。默认关闭，不切换现有模型入口。

## 信任与配置

DB 中的角色授权仍是事实源。一个员工对应其 AEP `kind=agent` 主体和专属 SA；不能使用
开启对话的用户或 owner 代替员工身份。可信发布者必须验证员工 CR UID、SA 名称及 UID
的实际关联，再使用既有 outbox/relay 发布 Grant。本阶段只消费已发布的可信 Grant，
不交付该生命周期写入者、业务发布器或 DeerFlow 的 token-file 消费。

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

## 有效 Pod 边界（第一阶段）

[#370](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/370) 的员工业务探针确认：
删除 Pod 后，API Server 的新 TokenReview 已拒绝旧令牌，热身份缓存仍可能接受它。
令牌未过期或 SA 未变化，不能证明原 Pod 仍然有效。

`ModelAccessGrant.spec.workloadPodUids` 及规范快照的同名字段承载可信发布者观察到的
有效 Pod UID 集合，最多 512 个。该字段可随新版本发布变化，位于不可变 binding 之外，
不会改变员工身份、路由绑定或配额 Consumer。会话发布不得携带这份工作负载证据。
快照要求排序、去重及有界 UID；relay 保留原样，并继续阻止同版本篡改与旧版本回放。

原生、不缓存结果的 OPA 边界要求同一已认证 bearer 的 Pod UID 属于当前集合。集合变化
只更新有绝对期限的外部策略，不重写 AuthConfig，也不清除身份或决策缓存；新 Pod 的新
令牌仍需首次 TokenReview。缺失/空集合拒绝所有工作负载请求，不能回落到“非空 UID”。

这是契约、relay 和执行器阶段；DB 证据登记、可信 Pod watch 及完整删除/替换业务验收
仍需后续实现，不能据此关闭 #370 或开启采用。先更新 CRD，再更新执行器和证据生产链路；
旧发布缺少该集合时会拒绝，实验入口应保持隔离。继续保留发布绝对期限、有界传播窗口及
`adoptionGate=false`。隔离场景的人工 Grant 夹具不等于生产生命周期证据已接通。

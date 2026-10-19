# 有效模型授权投影

简体中文 | [English](model-access-grants.md)

本阶段对应 [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338)，
承接[组件验证](auth-result-cache-feasibility.zh-CN.md)。在现有 kubebuilder 工程新增
`gateway.aep.zhiyuan.io/v1alpha1 ModelAccessGrant` 契约；不启用控制器、发布业务授权、
部署 Authorino 或切换默认网关。HTTP/SDK 不变，后续如有公开接口按 OpenAPI 优先实施。

## 事实源和范围

业务关系继续存 PostgreSQL；权限、模型、插件只赋予角色，人类和 agent 账号通过有效角色
及团队默认角色取得资源。CR 仅保存派生后的允许集合，不是直接给用户/团队授权的新入口，
不复制完整角色关系。现有 `ModelPolicy` 继续只承载限流配额，不授予模型访问权限。

每个投影绑定一个部署、账号和会话或可信工作负载。`subject.id` 是 AEP `users.id`，
包含 `kind=agent` 账号，不能使用数字员工 owner 代替。角色/团队变化只重发受影响投影，
无关投影及其有效热缓存保持不变。发布者按完整绑定生成确定性的哈希名称；名称、URL、
客户端请求头只能选择配置，不能证明身份。

- 人类会话绑定已验签模型 JWT 的 `sub`、`deployment_id`、`session_id`，保留 issuer、
  audience、用途、时间、scope 和当前会话状态检查，客户端不能自行声明允许模型。
- 员工绑定 agent 账号、专属 ServiceAccount UID、命名空间、受众和 DigitalEmployee UID。
  删除重建的账号/员工不能沿用旧绑定。须由原生可信验证器建立工作负载身份，CR 声明本身
  不认证 Pod；生命周期映射与验证器接入另阶段实现。CR 不含供应商 Key、应用模型令牌、
  密码、refresh token 或签名私钥。
- `models` 使用 AEP 模型 ID，空集合拒绝全部。`roleIds`/`teamIds` 仅用于保留原生配额与
  观测的可信成员身份，不是新的用户/团队资源授权。

## 发布和失效

`publication` 复用现有序号、操作、修订和内容摘要关联。生产者按不含摘要字段本身的规范
载荷生成 hash，并持久化不可变快照。DB→CR 继续复用事务 outbox 和 publication relay；
本阶段没有实现生产者或新增具备写权限的进程。

准入规则禁止修改部署、主体、绑定。内容改变必须增加发布序号；相同 spec 重复写可幂等，
同版本篡改与版本倒退拒绝。CEL 不能记住已删除对象的历史：删除重建仍须由持久 outbox/
历史保留护栏处理，保留禁用且模型集合为空的 tombstone，并以 RBAC 限制删除与归属接管。

`issuedAt`/`validUntil` 必须构成正向、最长一小时的发布有效窗口。这是准入上限，不是
默认允许断链一小时，也不是令牌寿命；发布者须采用更短、经评审的新鲜度界限，不能延长
会话、令牌或许可证有效期。热缓存命中也必须拒绝过期投影。准入只验证形状与时间顺序，
不验证当前时钟新鲜度或签名。

续期须从当前业务状态明确发布新版本，DB、许可证检查或交付链不可用时不能盲目延长旧许可。
接入阶段必须证明续期/新鲜度处理保留有效结果复用，不能用任意定期清缓存代替要求。
本契约不代表选定组件已满足该项能力。

用户角色、团队成员/默认角色、角色模型配置、模型与账号状态、必须改密、会话撤销、员工
删除/禁用和许可证变化都必须有事务信号。到期/失败处理和受影响主体展开仍待实现，直接
编辑测试 CR 不能证明上述业务链路。

## 观测与验收

`status` 为独立子资源。`Accepted` 表示语义接纳，`Applied` 表示归属资源写入；`Ready`
还须独立核对全部当前服务进程采用了当前发布，且证据未过期。记录 Pod UID 和进程实例号，
区分同 Pod 内容器重启。CR generation、Pod Ready、Lease leader、apply 成功都不能代替
该证据；观察者必须独立发现实际服务集合，不能用提交者给出的部分列表证明全量。本阶段
未实现观察者。

运行时必须拒绝缺失、删除、禁用、过期投影，拒绝跨绑定与模型越界，清洗身份头，保留原生
Consumer/Redis 哈希身份，继续逐请求/usage 计算配额。CR 变化不会自动终止已接纳的流。
允许首次命中冷副本、重启或驱逐后重新完整求值，有效热缓存及无关主体仍须复用。

生成命令为 `bash services/gateway-operator/generate.sh`。真实 envtest 覆盖版本/身份护栏、
撤权 tombstone、有效窗口和 status 隔离。采用前仍须完成业务 API→DB→outbox→relay→CR→
Operator→全部服务副本、乱序/断链/恢复、可信人类/员工绑定、容量/并发与原生配额兼容验收。
完成之前保留现有 authorizer。

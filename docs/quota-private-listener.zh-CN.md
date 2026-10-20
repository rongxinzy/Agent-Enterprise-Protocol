# 私有配额管理：原生资源渲染阶段

启用实验性的原生模型入口后，80 端口的模型鉴权也会拒绝合法的 ai-quota 管理
key，实际 AEP 配额 API 返回 `503 GATEWAY_SOURCE_UNAVAILABLE`。

共享渲染器新增 `NativeGatewayConfig.PrivateQuotaListener`，默认 false。本阶段
**没有**在 Operator 暴露开关或切换默认拓扑，尚未完成 #372。运行时接入、命名
空间 RBAC、资源清单与回收、全实例采用观测属于后续阶段，不能以手工部署这些
资源代替生产迁移。资源与依赖、复现命令和验收边界见
[英文部署说明](quota-private-listener.md)。

方案仅组合现成能力：Istio v1 Gateway/VirtualService、18081 ClusterIP Service、
Higress 原生 key-auth/ai-quota，以及只作用于管理 Host 和端口的 Istio v1
AuthorizationPolicy DENY。模型推理继续沿用原生 Redis 配额与 Consumer。
其他管理 Host、模型端口 80 不属于该 DENY 规则。

VirtualService 的 Authorization 匹配不能独立充当保护：ai-quota 能在 router
之前处理请求。实测移除 key-auth 后，仅有路由匹配时伪造管理员 refresh 返回
200；加入原生 DENY 后返回 403，余额不变。AEP 不自研鉴权引擎、配额计数、余额
加减或缓存。

私有模式拒绝包含 `*` 或换行的管理 key，避免 Istio 策略值把 key 中的星号当作
通配符；不合法配置在返回任何资源前失败，不改变旧模式的凭据处理。

本地探针使用独立随机 Kind 集群、真实 AEP/DB/outbox/relay/Operator、两副本
Authorino、Higress、Redis 与 Mock。Kind 只是测试工具，生产不依赖 Kind。
Higress 2.2.4 测试镜像的 discovery 使用 Istio 1.27 v1 API，1.19 CRD 不提供
v1，曾导致资源存在而监听未生成；现使用固定哈希的上游 1.27.0 原生定义。
这不代表已核实当前生产集群的 CRD 版本。

最终共享渲染器探针的本地 22 项检查通过且临时集群清理完成，覆盖原有主体限流、管理 read/refresh/
delta、缺失/错误/模型凭据及伪造 Consumer 拒绝、移除 key-auth 后仍拒绝、余额
不变、实际推理扣额、零余额拒绝及模型端口隔离。报告 `productRendererImplemented=true`、
`operatorPrivateRuntimeIntegrated=false`、
`networkPolicyEnforcementVerified=false`、`adoptionGate=false`。专用配额模式
也不等于后续完整会话变更/故障恢复基线通过。

探针从 SDK 读取实际期望路由，先回收旧配额资源，再通过既有 test-only Go
入口调用共享生产渲染器，由测试 apply 完整输出，一个原生配额插件同时服务
管理与推理；没有在 Operator 运行时启用新模式。

两次显式控制服务滚动重启后的首次推理与精确 Mock usage 扣额均通过，未重试
推理或余额变更。此联验依赖[配套部署阶段 PR #375](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/375)
的双副本、readiness 稳定期、零不可用
与原生 preStop 排干配置。此前单控制 Pod 的探针曾偶发 401/403；捕获的失败
中，JWT 未过期且主体/会话匹配，余额仍为 7，ext_authz 拒绝计数增加，Authorino
记录 JWKS 连接/签名校验失败，不能归因为配额计算或私有 RBAC 策略。复现前先
应用配套的控制服务滚动发布阶段。

旧连接采用、Redis 故障语义、生产网络隔离、凭据发布器与全实例 readiness 尚未
由此证明。Envoy 可能保留无活动引用的 ECDS 缓存，移除断言以活动过滤链为准。

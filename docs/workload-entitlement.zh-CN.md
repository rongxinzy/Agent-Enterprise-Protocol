# 员工工作负载的许可证发布契约

简体中文 | [English](workload-entitlement.md)

这是既有 Kubebuilder access Operator 的显式启用 Kubernetes 契约，不新增 HTTP API，
不替代人类用户激活流程，也不把 Kubernetes 身份令牌伪装成 AEP entitlement JWT。

## 证据与信任

部署要求 entitlement 时，启用的员工工作负载 Grant 必须有 `spec.workloadEntitlement`：
具体的 `licenseId`、`licenseDigest` 及固定功能 `enterprise.models`。ID 和摘要必须同时
匹配 `spec.licenses` 的一项。只有 Kubernetes 绑定可携带此声明。准入拒绝未知许可证、
错误摘要、其他功能及同版本篡改。

可信 DB producer 必须在读取账号有效角色授权和可信工作负载观察的同一加锁快照中，
选择已登记、验签通过、匹配部署、有效且明确包含 `enterprise.models` 功能的许可证。
不能从“某张许可证有效”推断功能授权，不能用员工 owner、会话发起人或请求头挑选
身份及许可证。

此声明不是签名许可证、bearer 凭据、用户激活记录或额外的用户直接授权。与模型允许
集一样，它依靠独占 writer 的 RBAC 及既有不可变发布/outbox/relay 护栏保持可信。
CR/status 写权限属于特权；资源及员工 Pod 均不应包含 provider key 或许可证私钥。

## 执行与生命周期

原生 TokenReview 验证实际 Pod-bound ServiceAccount bearer；原生 OPA 独立执行身份、
模型与被选许可证的绝对宽限截止时间，身份和完整授权结果已热缓存时也必须检查。
许可证无截止时间表示永久。接受列表中的另一张有效许可证不能自动替代被选 ID/摘要。

更换许可证须发布新版本并明确选择新许可证；选择进入当前绑定的语义 hash，单纯发布
续期不重写 AuthConfig。许可证撤销、删除、移除功能或到期时，producer 必须发布停用、
模型集合为空的 Grant，并移除声明。即使 `RequireEntitlement=true` 且已无许可证，拒绝
tombstone 仍可下发。非法声明由准入及渲染器拒绝，不能静默忽略。

entitlement 继续默认要求，workload audience 仍默认关闭，不切换默认流量；缺少声明的
启用工作负载在要求 entitlement 时继续拒绝。人类仍使用绑定具体许可证 ID/摘要和会话
的已签名 AEP entitlement JWT。

自然到期由请求时的绝对时间检查保证。主动撤销仍依赖发布传播并受 Grant 新鲜度约束，
不是零延迟撤权；TokenReview 缓存的 Kubernetes 生命周期限制也仍存在。

## 验证与发布顺序

不可变规范快照包含该声明，篡改或删除声明会改变内容 hash，但不改变工作负载绑定
标识。既有 relay 原样传递，拒绝同版本篡改及读回字段被裁剪。本阶段尚未实现业务
producer 的已验签许可证选择及自动生成声明。

[原生工作负载探针](workload-native-access.zh-CN.md) 增加
`AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT=true`，以显式 CR 夹具和两个真实原生验证进程
检查热缓存宽限到期、不借用其他有效许可证、明确替换、拒绝 tombstone 及无关员工不变。
它不证明 DB 验签、自动发布、运行时交付、Higress 推理、Redis 扣额或全部服务副本采用。

先升级生成的 CRD、access Operator 和快照/relay 读取端，再启用发布此字段的 producer。旧 Operator 在要求
entitlement 时拒绝启用工作负载；旧 CRD 可能裁剪未知字段，投递必须精确读回验证，
不能只看 API 写入成功。业务与故障集成验收通过前，`adoptionGate=false`。

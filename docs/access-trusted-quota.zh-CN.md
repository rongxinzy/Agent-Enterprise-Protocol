# 原生授权入口的可信身份与 Redis 限流

本阶段在[会话选择路由](access-binding-routes.zh-CN.md)之后，补齐专用实验入口
的身份交接。保持 opt-in；不切换既有 gateway-authorizer，不把 CR 写入成功、
Pod Ready 或单次请求成功当作全实例采用证据。

## 数据路径

```text
客户端 JWT + 不可信会话选择提示 + 请求正文
  → Operator 管理的会话选择路由
  → Envoy 原生 gRPC ext_authz
  → Authorino JWT / 缓存授权 / 每请求有效期与实际模型检查
  → 可信身份写入 ext_authz dynamic metadata
  → 原生 HeaderMutation 清除全部调用方 x-aep-*、Consumer 与 bearer
  → Envoy Lua 把可信 metadata 复制为现有插件需要的请求头
  → 既有 ModelRoute / ai-proxy / 原生 Redis 限流插件
  → 供应商
```

鉴权、授权缓存与 Rego 执行均属于 Authorino；请求和 Token 计数、窗口与拒绝
属于现有 Higress 原生插件及 Redis。Lua 只复制经过授权的 metadata，不实现
JWT 验证、角色继承、授权策略、业务 DB 查询、结果缓存或计数公式。
没有成功授权 metadata 的请求直接拒绝，不能落到可调用供应商的路由。

原生 HeaderMutation 必须在 ext_authz 之后、Lua 之前；后续 ai-proxy 才注入
供应商凭据。不能在 router 阶段删除 Authorization，否则会误删供应商密钥。
响应同时清除保留前缀与 Consumer 头，防止上游把内部头带回客户端。

## 复用既有身份约定

- Consumer 使用既有 `gatewaysource.Consumer(deployment, user)`；同一用户的
  不同会话共享余额身份，不使用客户端提供的 Consumer 或配额管理员身份。
- 用户、部署、会话、有效团队和有效角色来自已发布 `ModelAccessGrant`。
  资源仍然只授权给角色；这些用户/团队信息是派生的配额、观测主体。
- 模型取实际请求正文，必须同时属于当前授权集和 JWT 的 `model_scopes`。
- `x-aep-limit-*` 与既有 `gatewaypolicy.Headers` 相同，只携带常量 `1` 的
  主体存在标记，分别覆盖全局、用户、团队、角色及其模型范围。
- 原生 OPA 的 SHA-256、base64url 无填充内置函数生成现有 key；不引入第二套
  Redis 命名、窗口或统计算法。每请求仍遵循现有最多 80 个主体标记的上限。

模型相关的标记在选定实际模型后生成，而非为 512 个模型分别展开整套响应头。
Authorino 响应头名称是静态配置，因此通过原生 metadata 交接动态 map。
团队/角色、模型集变化仍改变受影响主体的语义 hash；单纯续期不更新 AuthConfig，
不因为无关用户变化重建当前用户的授权缓存。

## 可重复验收入口

先按[现有验收框架](model-access-operator.zh-CN.md)准备固定版本镜像、工具及
Authorino CRD，并在本地运行：

```sh
AEP_NATIVE_ACCESS_BUSINESS=true \
AEP_NATIVE_ACCESS_HIGRESS=true \
AEP_NATIVE_ACCESS_MANAGED=true \
AEP_NATIVE_ACCESS_POLICY_SOURCE_DIR=deploy/kubernetes/access-policy-source \
AEP_AUTHORINO_CRD_PATH=/path/to/authorino.kuadrant.io_authconfigs.yaml \
node tests/e2e/native-access-projection.mjs
```

该入口使用随机独立 Kind 集群、真实 PostgreSQL/outbox/relay、Kubebuilder
Operator、两副本 Authorino、Higress 2.2.4、Redis 和 Mock 供应商。
默认业务验收仍不启用 managed 入口。夹具的 Kubernetes 写权限只在 managed
模式授予，避免把缺少部署权限误判为路由或鉴权缺陷。

Redis 通过 Higress 的单例 `McpBridge/default` 注册。验收必须看到实际 CDS
中的 Redis cluster，并同时确认当前限流 ECDS 规则、模型映射、模型 RDS 和
上游 CDS；仅有 Pod Ready 或限流插件的更新计数不足。多资源 publication
切换期间既有 Operator 会暂时回收不匹配的模型路由，因而可能返回拒绝/404。
本验收等待整个快照收敛后检查限流，不代表配置更新已经做到零中断。
每次推理断言使用新的 HTTP 连接，验证当前活动 listener。新增 WasmPlugin
涉及 LDS 过滤链变化；旧 keep-alive 连接可能继续持有旧过滤链。本次复现过
首请求未产生计数、后续连接才计数的现象，因此现有连接排干及全部连接采用
仍是独立门槛，不能据此报告生产全连接已经执行新配额。

2026-10-20 本地完整 Mock 运行通过 29 项检查：包含六种请求范围、两种 Token
范围、团队/角色共享及用户隔离、精确 Mock usage 计数、伪造身份拒绝、删除规则
恢复，以及原有业务撤权、会话/账号/模型禁用、续期、控制/授权进程故障与恢复。
报告为 `completed=true`、`cleaned=true`、`quotaIdentityVerified=true`，并显式标记
`existingConnectionAdoptionVerified=false`。该结果不等于累计配额、员工或生产
全连接验收完成。

真实云端模型必须单独显式选择 `AEP_NATIVE_ACCESS_REAL_PROVIDER=deepseek`，
通过不回显的 stdin 提供临时运行时密钥。密钥不进入环境变量、构建命令、测试
源码、报告或日志。只发起一次可计费请求，模型为 `deepseek-flash`；后续请求
应被原生 Token 限流挡住。验收结束删除临时模型、授权、Credential 和 Secret，
最终删除本次独立集群。不要在自动 CI 配置真实密钥。

2026-10-20 显式真实模式完整重跑通过 30 项检查。DeepSeek Flash 返回 HTTP 200，
`prompt_tokens=9`、`completion_tokens=1`、`total_tokens=10`；Higress 原生 Redis
Token 计数严格等于 10，下一次携带伪造 Consumer/用户头的请求返回 429。
报告 `realProvider.completed=true`、`tokenLimitDenied=true`、`completed=true`、
`cleaned=true`，临时模型、角色授权、规则、Credential、Secret 和独立集群均已清理。
验收在 Control 和授权进程故障后重新连接本地 port-forward，并重新发现实际进程；
不会把失效的测试端口误判为产品链路故障，也没有删除原有故障或拒绝断言。
该真实场景为非流式文本，供应商 Secret 由测试直接供给；不扩大到凭据发布器、
流式、多模态、员工身份或生产全连接验证。

## 验收边界

该场景验证可信 Consumer、主体标记和既有 Redis 请求/Token 限流插件的组合。
`WasmPlugin.failStrategy=FAIL_CLOSE` 不能当作 Redis 错误时必然关闭的保证：
本次错误服务发现夹具中，没有 Redis CDS cluster 时，固定请求插件仍返回
200 且不产生计数。该失败记录不算限流通过；生产采用前仍需单独验收 Redis
不可用语义及部署前置检查，不能用 CR/插件加载成功宣称配额已受保护。
累计余额 `ai-quota` 的管理 API 与实验 listener 的隔离接入仍需独立验收；不能
把窗口 Token 限额称作完整余额账本，也不能把测试直接供给的供应商 Secret
称作 credential-publisher 交付验收。

员工 Kubernetes 工作负载身份、生产全实例采用观测、容量、多模态与默认入口
切换仍不在本阶段。`Ready`、`adoptionGate` 不因此变为 true。回退按会话路由
手册先隔离流量、撤销实验授权并等路由回收，再撤销组件及权限。

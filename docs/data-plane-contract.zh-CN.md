# 数据面契约

M3 数据面契约把企业租户的期望状态与网关 reconciler 的观察状态分开。

`POST /aep/v1/admin/data-plane/publish` 是推荐的发布路径，让模型目录成为网关路由的单一事实源。服务端从每个 `sourceType: gateway`、`protocol: openai-compatible` 且 endpoint 与上游模型完整的启用目录模型派生路由，并原子替换期望状态。绑定 Credential 的模型映射到约定 Secret `aep-credential-<credentialId>`（键 `api-key`，命名空间 `higress-system`）；Secret 值由部署侧 Secret 系统供给，永远不经过该 API。派生路由使用 `openai` provider 类型。派生状态与已存状态一致时发布是幂等空操作；否则服务端分配按内容寻址的 `catalog-` revision，除非调用方显式提供 `revision`。

`PUT /aep/v1/admin/data-plane/desired-state` 是已弃用的手工逃生口，用于运维手工编写的状态（例如需要 Higress 原生 `deepseek` provider 类型的路由）。它接收确定性的 `revision` 和模型路由。路由可以引用 Kubernetes Secret 或外部 Secret 的 `name`、`namespace`、`key`，接口永远不接收或返回供应商 Secret 明文。重复发布相同 revision 必须幂等。两个入口写入同一份期望状态，内容哈希语义保持不变。

`GET /aep/v1/admin/data-plane/status` 返回 `pending`、`applying`、`ready`、`degraded` 或 `error`，以及观察到的 revision、内容哈希、资源数量和有界错误信息。其 `catalogComparison` 字段列出目录可发布但期望路由中缺失的模型、目录不会再发布的期望路由，以及逐字段的路由不一致项，供控制台展示未发布或已漂移的模型。不一致项描述的是一次目录发布会改变的内容，不一定是错误。状态接口只提供观测，不授予访问 Secret 明文的能力。

PR #17 将实现消费该契约的 reconciler：只应用期望 revision，计算规范化 SHA-256 内容哈希，凭证失败时关闭数据面，并在不记录路由 Secret 的前提下写回状态。

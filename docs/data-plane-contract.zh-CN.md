# 数据面契约

M3 数据面契约把企业租户的期望状态与网关 reconciler 的观察状态分开。

`PUT /aep/v1/admin/data-plane/desired-state` 接收确定性的 `revision` 和模型路由。路由可以引用 Kubernetes Secret 或外部 Secret 的 `name`、`namespace`、`key`，接口永远不接收或返回供应商 Secret 明文。重复发布相同 revision 必须幂等；发布新状态必须使用新的 revision。

`GET /aep/v1/admin/data-plane/status` 返回 `pending`、`applying`、`ready`、`degraded` 或 `error`，以及观察到的 revision、内容哈希、资源数量和有界错误信息。状态接口只提供观测，不授予访问 Secret 明文的能力。

PR #17 将实现消费该契约的 reconciler：只应用期望 revision，计算规范化 SHA-256 内容哈希，凭证失败时关闭数据面，并在不记录路由 Secret 的前提下写回状态。

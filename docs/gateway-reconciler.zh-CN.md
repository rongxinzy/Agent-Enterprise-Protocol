# Gateway Reconciler

`services/gateway-reconciler` 是独立于 control-service 的进程。它按租户轮询内部数据面接口，写入 `applying`、`ready` 或 `error` 状态，并把确定性的 Higress 资源原子渲染到输出目录。

必须配置 `AEP_RECONCILER_CONTROL_URL`、`AEP_DATA_PLANE_RECONCILER_TOKEN` 和 `AEP_RECONCILER_TENANTS`。共享 Token 只通过 `X-AEP-Data-Plane-Token` 发送，租户身份通过 `X-AEP-Tenant-ID` 发送。期望状态 schema 不接受供应商明文，渲染器也不会输出明文。

Worker 对失败租户使用有界指数退避重试。输出文件通过临时文件加 rename 写入，网关消费者不会看到半成品。PR #18 将补齐生产 Kubernetes 部署、RBAC、TLS、高可用和外部 Secret 接入。

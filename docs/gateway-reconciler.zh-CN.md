# Gateway Reconciler

`services/gateway-reconciler` 是独立于 control-service 的进程。它按租户轮询内部数据面接口，写入 `applying`、`ready` 或 `error` 状态，并把确定性的 Higress 资源原子渲染到输出目录。

必须配置 `AEP_RECONCILER_CONTROL_URL`、`AEP_DATA_PLANE_RECONCILER_TOKEN` 和 `AEP_RECONCILER_TENANTS`。共享 Token 只通过 `X-AEP-Data-Plane-Token` 发送，租户身份通过 `X-AEP-Tenant-ID` 发送。期望状态 schema 不接受供应商明文，渲染器也不会输出明文。

Worker 对失败租户使用有界指数退避重试。输出文件通过临时文件加 rename 写入，消费者不会看到半成品。它支持通过 `AEP_DATA_PLANE_RECONCILER_TOKEN_FILE` 读取挂载 Secret；直接值与文件形式不能同时设置。

生产 Kustomize 基线提供两个加固后的 reconciler 副本、受限 Higress RBAC、TLS 入口、NetworkPolicy、PDB 和 External Secrets 集成。配置 `AEP_RECONCILER_KUBERNETES_URL` 及 service-account token 后，reconciler 会通过 Kubernetes server-side apply 下发确定性的 Ingress 与 Higress WasmPlugin。两个副本使用相同 field manager 和期望对象，重复写入幂等，周期收敛会修复漂移。没有启用路由时会删除其持有的 Ingress，并把插件 match rules 收敛为空。见 [production-data-plane.zh-CN.md](production-data-plane.zh-CN.md)。

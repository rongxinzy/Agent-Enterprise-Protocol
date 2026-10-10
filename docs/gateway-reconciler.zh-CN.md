# Gateway Reconciler

`services/gateway-reconciler` 是独立于 control-service 的进程。它按部署轮询内部数据面接口，写入 `applying`、`ready` 或 `error` 状态，并把确定性的 Higress 资源原子渲染到输出目录。

必须配置 `AEP_RECONCILER_CONTROL_URL`、`AEP_DATA_PLANE_RECONCILER_TOKEN` 和 `AEP_RECONCILER_TENANTS`。共享 Token 只通过 `X-AEP-Data-Plane-Token` 发送，部署身份通过 `X-AEP-Deployment-ID` 发送。期望状态 schema 不接受供应商明文，渲染器也不会输出明文。

Worker 对失败部署使用有界指数退避重试。输出文件通过临时文件加 rename 写入，消费者不会看到半成品。它支持通过 `AEP_DATA_PLANE_RECONCILER_TOKEN_FILE` 读取挂载 Secret；直接值与文件形式不能同时设置。

`/livez` 只表示进程仍可提供 HTTP。`/readyz` 启动时返回 `503`，只有全部已配置 deployment 至少成功同步一次后才返回 `200`。之后任一 deployment 获取、渲染、输出、Kubernetes apply 或状态回报失败，都会立即使该 deployment 和整个进程变为未就绪；下一次成功同步后自动恢复。这样依赖故障不会触发 liveness 重启，但编排器会把异常副本移出服务。

生产 Kustomize 基线提供两个加固后的 reconciler 副本、受限 Higress RBAC、TLS 入口、NetworkPolicy、PDB 和 External Secrets 集成。配置 `AEP_RECONCILER_KUBERNETES_URL` 及 service-account token 后，reconciler 通过 `aep-system/gateway-reconciler-leader` Lease 选出唯一领导者，再通过 server-side apply 下发 Ingress 与 Higress WasmPlugin。跟随者保持空闲和就绪以便接管，只有领导者通过 readiness 报告同步失败。ServiceAccount 需要 Lease 创建权限，以及这个固定名称的 get/update 权限；仅成功获取或续租才获得领导权，权限拒绝或冲突时停止同步。两个副本使用相同 field manager 和期望对象，重复写入幂等，周期收敛会修复漂移。没有启用的 openai-compatible 路由时会删除其持有的 Ingress，并把插件 match rules 收敛为空。见 [production-data-plane.zh-CN.md](production-data-plane.zh-CN.md)。

协议为 `anthropic` 的路由采用另一种渲染：每个启用模型获得自己的单路径 Ingress `aep-anthropic-<suffix>`（客户端路径前缀 `/<净化后模型ID>`，名义 `aep-model-gateway` 后端）和同名 EnvoyFilter，透传逻辑全部由 EnvoyFilter 承载——由 endpoint 派生的 STRICT_DNS 上游集群（https 带 TLS + SNI）、把 ingress 路由重定向到该集群的 route merge、Host 改写、把模型前缀换成 endpoint 自身路径的正则改写，以及来自路由 credentialRef 的服务端凭证头（`x-api-key` 与 `authorization`）。全程不触碰共享资源（McpBridge、共享插件配置），租户渲染因此不会与手工或 chart 维护的路由打架。禁用路由会删除整对资源；凭证未解析时省略凭证头，由上游自己回答 401。

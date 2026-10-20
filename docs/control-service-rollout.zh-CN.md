# 控制服务滚动发布可用性

生产控制服务维持双副本，显式配置 `maxUnavailable: 0`、最多增加一个副本，
新 Pod Ready 后稳定 5 秒再继续滚动。Kubernetes 原生 `preStop.sleep` 保持
终止中的服务继续响应 5 秒，给 EndpointSlice 摘除与网络传播留出窗口，之后
才发送 SIGTERM 并执行现有 HTTP 优雅关闭。45 秒终止预算包含此 hook。

这同时保护控制 API 与身份发现/JWKS。即使 JWT 未过期，冷缓存的 Authorino
实例若向已经停止的端点获取 JWKS，也会失败关闭；已有缓存不能保证所有实例
都有当前用户与公钥的暖缓存。此前接受的冷缓存例外仍然成立。

原生 Sleep 生命周期在 Kubernetes 1.34 起稳定，1.30–1.33 需要启用
`PodLifecycleSleepAction`。交付较旧集群前先验证版本与功能门；distroless
镜像不需要新增 shell/sleep 程序，也不添加自研 JWT 缓存、校验重试或应用延时。
版本边界可核对上游 [Kubernetes 功能门定义](https://github.com/kubernetes/kubernetes/blob/v1.35.0/pkg/features/kube_features.go)。

5 秒是有界部署默认值，不代表所有生产网络一定在 5 秒内收敛。需要结合实际
CNI、DNS 与负载验证滚动过程，在评审后的交付 overlay 中统一调整稳定期、
排干时间与终止预算。readiness、liveness 和依赖检查语义不变，节点突然故障
与网络中断不属于优雅替换的保证。

本地原生访问验收使用相同参数、真实控制服务/PostgreSQL/Authorino，不部署
生产集群。参见[英文说明](control-service-rollout.md)。

独立的 managed 业务基线通过 29 项检查并清理自有集群，覆盖角色/团队/用户变更、
会话撤销、原生请求/Token 限流、控制服务停机到期、恢复与 Authorino 替换。
另一次与私有配额渲染器的联验通过 22 项，包含两次控制服务滚动后首次推理及
精确 Mock usage 扣减。这些隔离测试不等于生产采用证据，原生鉴权采用门仍关闭。

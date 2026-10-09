# 原生限流与配额部署

AEP PostgreSQL 仅保存规则、乐观版本与发布快照，不保存计数器。编辑/删除是草稿，
调用 `POST .../limits/publish` 后才进入 reconciler。删除保留禁用 tombstone，以确保
旧插件失效；单部署最多 100 个规则 ID，后续策略调整应复用禁用规则，不重置 Redis。

先启用身份指南中的调用时成员查询。authorizer 的可信 `X-AEP-Limit-Keys` 表达
用户、模型、全部有效团队/角色及模型限定组合。每条规则独立渲染为固定 OCI digest
的 `cluster-key-rate-limit` / `ai-token-ratelimit`，不依赖插件内部多条规则的短路行为。
执行和计数由原生插件与 Redis 负责，AEP 不实现本地 token 计数或限流引擎。

给 reconciler 挂载运维配置文件，设置 `AEP_RECONCILER_NATIVE_GATEWAY_CONFIG_FILE`。
样例为 `deploy/kubernetes/monitoring/gateway-native-config.example.json`。Redis 是新增
的插件依赖，Prometheus/Grafana 无法替代计数器存储；Redis 与管理凭据通过已有 Secret
系统部署到 `higress-system`，只有 reconciler 能解析，API 不返回凭据。

规则发布状态为 pending/applied/error。applied 只确认 Kubernetes apply 和对应版本
回执，`runtimeVerified` 仍为 false，不把 apply 成功当成 Redis 连通和实际限流证明。

提供 `quotaAdminCredentialRef` 后渲染 ai-quota 与独立 key-auth 管理路由。设置
`AEP_GATEWAY_QUOTA_URL=http://<内部 Higress>/aep-quota-<部署资源后缀>/v1/chat/completions/quota`
及 `AEP_GATEWAY_QUOTA_TOKEN_FILE`（与原生 key-auth 管理 Secret 相同）。管理路径保留
ai-quota 原生要求的 `/v1/chat/completions/quota` 后缀，客户端不能伪造管理员 Consumer。

配额 API 校验部署内用户、服务端生成 Consumer，直接调用原生 GET/refresh/delta。
原生写操作返回文本后，AEP 再读原生余额，不自行加减。如果 delta 已发送但后续读取
失败，结果可能不确定，禁止自动重试。原生 Token/配额继承插件完成请求后的计数及
并发语义，不宣称是交易级账本。部署配置为可选，不更改默认生产拓扑。

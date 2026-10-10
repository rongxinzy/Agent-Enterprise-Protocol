# 原生限流与配额部署

AEP PostgreSQL 仅保存规则、乐观版本与发布快照，不保存计数器。编辑/删除是草稿，
调用 `POST .../limits/publish` 后才进入 reconciler。删除保留禁用 tombstone，以确保
旧插件失效；单部署最多 100 个规则 ID，后续策略调整应复用禁用规则，不重置 Redis。

先启用身份指南中的调用时成员查询。authorizer 为用户、模型、有效团队/角色及模型
限定组合生成独立的可信 `X-AEP-Limit-<SHA256>` 存在标记（值固定为 `1`）。原生
`limit_by_header` 精确匹配标记，共享团队计数不会因用户、模型或其他成员关系分裂。
每次调用最多 80 个标记，即最多 38 个不同团队/角色成员关系；超过上限拒绝调用，
为 Envoy 默认的 100 个请求头限制保留标准请求头空间。每条规则独立渲染为固定 OCI digest
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

Token 限流及配额固定到 Higress 官方插件 **2.0.3**，来源为
[2.2.5 发布快照](https://github.com/higress-group/higress/blob/2ba624cc479dd28bb88a853d6889e7931b993537/plugins/release/snapshots/2.2.5.json)，
构建源码为 `2b837c0ada8dbfb3e4bd92fc3f18ea532d269cb1`：

| 插件 | OCI manifest digest |
| --- | --- |
| ai-token-ratelimit | `sha256:9276a7d4cbd7afef668fd1aaf41212e663fd7fa98661c619398a7f2fb2736679` |
| ai-quota | `sha256:2684810410de2803200f21d4971fe30161a7c0060c39ccab3906f6bcd4b8a509` |

此前 2025 年 6 月的 Token 产物不支持 `global_threshold`，报 `missing rule_items`
并通过 FAIL_CLOSE 返回 500；旧配额产物不识别 Anthropic 路径和 usage，零余额也能调用。
新版配额产物包含上游非流式扣减修复及原生 Anthropic 支持，计数、扣减、拦截仍由插件
与 Redis 执行，AEP 不增加计算逻辑。

运行 `npm run test:e2e:gateway-native`：使用生产渲染器、真实 Higress 2.2.4 与 Redis、
mock OpenAI/Anthropic 上游，验证 JSON/SSE 配额扣减、零余额拒绝、全局/团队共享
Token 限流、禁用恢复和 Redis 故障。该命令已纳入标准 Compose E2E 门禁；只适配
standalone 测试路由与服务发现，不等同于 Kubernetes Pod 通信或生产监控部署验收。

新版 Token 插件采用累计计数及不同 Redis 键布局，旧版剩余额度计数的周期窗口不会
继承，升级应由运维选择发布窗口。配额沿用 `aep_quota:` 和原 Consumer 编码，不迁移
余额或自行加减。实际部署验收前，`runtimeVerified` 保持 false。

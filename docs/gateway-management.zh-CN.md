# 原生模型网关管理接口

`/aep/v1/admin/model-gateway` 在现有 AEP 会话与部署权限范围内接入 Higress、
Prometheus 和 Loki。监控接口返回来源结果，SDK 和 AEP 不计算用量、比例、
组织小计、延迟百分位或成本。P95/P99、成本与会话统计维度不纳入本次契约。

先读取 `capabilities` 再开放前端控件。未配置来源返回 503
`GATEWAY_SOURCE_UNAVAILABLE`；来源不支持的维度返回 422
`GATEWAY_DIMENSION_UNSUPPORTED`。缺失数据不显示零值。

| 接口 | 用途 |
| --- | --- |
| GET capabilities / health | 能力发现、来源状态与原生健康 |
| GET metrics | 有界时间范围的原生查询结果；支持来源实际提供的维度与筛选 |
| GET requests / requests/{requestId} | 原生日志分页与请求关联；游标使用来源纳秒时间戳 |
| GET limits、GET/PUT/DELETE limits/{ruleId} | 原生规则配置；创建 expectedVersion=0，修改/删除携带已读版本 |
| POST limits/publish、GET limits/status | 发布与配置应用状态；apply 成功不代表运行时已强制执行 |
| GET quotas/{userId} | 查询原生 ai-quota 余额，不用总额减用量估算 |
| POST quotas/{userId}/refresh、/delta | 刷新或增减原生配额；增减不自动重试 |
| POST models/{modelId}/test-access | 单个已启用且已授权模型的短期测试访问 |

实际测试调用继续经过 authorizer → Higress → provider；不新增推理代理或执行内核。
目录、授权、健康探测、组织、通用事件和数据面接口继续复用。组织日志字段必须来自
可信身份；不采集提示词、回复正文或凭据。接口存在不代表部署已配置日志库或配额插件。
# 原生数据源配置

通过 `AEP_GATEWAY_PROMETHEUS_URL`、`AEP_GATEWAY_LOKI_URL` 连接已有监控服务；
可选的 `*_TOKEN` / `*_TOKEN_FILE` 提供数据源服务凭据，不转发浏览器令牌。
源请求拒绝重定向，超时 8 秒，响应上限 4 MiB，并传递可信部署的 `X-Scope-OrgID`。

AI 指标还通过 authorizer 编码后的部署/用户 Consumer 约束查询。
`ai_model` 是插件实际记录的上游模型标签；不会伪造目录模型名称。
`calls` 使用原生 `llm_duration_count`（带用量的完成请求），不代表 authorizer
拒绝请求也有 token 用量；旧的 `none` Consumer 无法追溯归属用户。

Envoy 连接管理器/集群指标没有 Consumer 标签。只有配置
`AEP_GATEWAY_METRICS_DEPLOYMENT=<部署 ID>`，且数据源仅包含该部署的推理网关与
authorizer 指标时，才能查询 QPS、非 5xx 比率和鉴权状态计数。共享数据源返回 422。
非 5xx 比率不是模型业务成功率。原生 Prometheus 插件标签没有团队/角色。
配置身份阶段提供的元数据 Vector/Loki 采集链路并开启
`AEP_GATEWAY_ORGANIZATION_LOGS=true` 后，组织查询由 Loki 原生 count/sum 范围函数
执行。采集器只复制调用时成员元数据，不计算数值；筛选总量使用 base 流，组织分组
使用成员流，整体总量不会从重叠团队相加得到。日志失败表示 HTTP >=400，与
ai-statistics 的模型响应体失败计数不同；返回结果保留来源，不混淆两种口径。

Loki 访问日志流需要 `aep_deployment_id` 和 `aep_source=gateway|authorizer` 标签，
仅采集部署指南允许的元数据。查询只输出请求/模型/用户、成员关系、状态、token 和
原生耗时字段；不返回提示词、回答、工具参数、凭据或自由文本的供应商错误。
AEP 不另存监控历史。

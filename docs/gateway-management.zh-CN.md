# 原生模型网关管理接口

## 指标口径契约

指标响应包含 `source` 和必填 `definition`：`id`、`unit`、`aggregation`、
`windowSeconds`、`groupBy`、`modelDimension`。这些字段描述原生查询，不计算监控值。
SDK 原样保留来源结果及口径；前端按口径标注，不能拼接不同口径的曲线或自行累计序列点。

| 指标 | Prometheus 定义 | Loki 定义 |
| --- | --- | --- |
| calls | `ai_usage_completed_calls`：插件记录了 usage/耗时的完成请求 | `gateway_access_requests`：网关访问日志请求 |
| failures | `ai_detected_failures`：插件检测的模型响应失败 | `gateway_http_errors`：HTTP 状态 ≥400 的网关日志 |
| input_tokens / output_tokens | `ai_input_tokens` / `ai_output_tokens`：原生 usage 计数器增量 | `gateway_log_input_tokens` / `gateway_log_output_tokens`：原生日志 usage 字段的范围统计 |
| first_token_duration / service_duration | `ai_usage_mean_first_token_duration` / `ai_usage_mean_service_duration`：时长计数器与次数的速率均值 | `gateway_log_mean_first_token_duration` / `gateway_log_mean_service_duration`：有对应耗时字段的日志均值 |

QPS 定义为 `envoy_downstream_qps` / `envoy_upstream_qps`；原有 success_rate 名称对应
`envoy_downstream_non_5xx_ratio` / `envoy_upstream_non_5xx_ratio`，单位是 0–1 比率，
不能标为模型业务成功率。`authorizer_http_requests` 是按 HTTP 状态分组的鉴权服务请求增量。

目录 `modelId` 筛选和团队/角色查询使用 Loki；Prometheus 按模型分组表示上游模型，
Loki 表示 AEP 目录模型，响应通过 `modelDimension` 明确区分。维持相同口径的查询应携带
之前返回的 `expectedDefinition=definition.id`；来源选择导致口径变化时返回 422
`GATEWAY_METRIC_DEFINITION_MISMATCH`，不查询来源、不自动降级。

`windowSeconds` 是每个点的原生回看窗口：速率及速率均值使用 120 秒，其他指标使用
`step`（默认 60 秒）。`step` 是输出采样间隔，两者不一定相同；计数曲线不是整个所选
时间范围的总量，不能由前端求和充当汇总。缺失 usage、空序列和未知值不转为零。

本次补的是 AEP 接入契约。Higress/ai-statistics 已有原生观测输出，Prometheus/Loki
提供查询，Console 已有插件配置、ai-quota 已有余额管理 API；不新增对应执行或统计引擎。

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

## 主动模型测试

`POST models/{modelId}/test-access` 要求 `models.read` 权限、有效会话及当前模型
分配。管理员同样不能绕过模型分配。仅支持已启用的网关模型；不会接受客户端传入
测试地址。返回部署配置的网关地址、协议路径和单模型访问凭证，有效期最多两分钟。
生产环境复用已登记、未吊销、包含 `enterprise.models` 的 License 及 entitlement
校验；有效期还受 License 到期/宽限期约束。开发环境允许现有模型 JWT 协议。

浏览器 Admin Console 使用凭证直接调用 authorizer → Higress → provider，测试
结束后丢弃，不写入日志或持久存储。Electron renderer 仍遵守原有主进程令牌边界。
OpenAI 测试调用 `baseUrl + /chat/completions`，Anthropic 调用返回的模型路径
`baseUrl + /v1/messages`。AEP 不生成模型响应、不计算 token，也不伪装成新的
Higress 原生测试插件；此接口只为既有推理链路签发受限凭证。

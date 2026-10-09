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

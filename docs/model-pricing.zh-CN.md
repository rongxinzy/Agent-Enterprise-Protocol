# 模型参考价格

`GET /aep/v1/admin/models/{modelId}/pricing` 需要 `models.read`，返回
`modelId`、`version`、可空的 `pricing` 和 `updatedAt`。模型存在但尚未保存价格时，
返回版本 `0`，价格与更新时间为 null。

同一路径的 `PUT` 需要 `models.write`，请求包含 `expectedVersion` 和 `pricing`。
价格使用非负十进制字符串，最多九位整数、六位小数，单位为**每百万 Token**。
币种为三位大写代码，输入和输出单价必填；缓存命中输入单价、纯文本来源说明可选。
明确配置零表示免费；未配置表示未知。接口不进行汇率转换。
本地和云端模型使用相同的参考价格配置，这不代表已测量 GPU 或电费成本。

保存时提交读取到的版本；并发修改返回 HTTP 409 `MODEL_PRICING_VERSION_CONFLICT`，
应重新读取并由管理员明确决定如何保存。清除价格时提交 `pricing: null` 和当前版本；
清除后版本继续递增，防止旧请求恢复已清除配置。不存在或其他部署的模型返回 404；
非法配置返回 400 `INVALID_MODEL_PRICING`。沿用 AEP 会话和
`X-AEP-Protocol-Version: 1.0` 请求头。

这些接口仅保存管理员维护的参考价格，不自动发布给 Higress，不改变推理、限流或配额，
不计算成本、不重算历史请求，也不启用原生成本指标。监控页须继续在真实运行时计费
来源接通前将成本显示为“未提供”。

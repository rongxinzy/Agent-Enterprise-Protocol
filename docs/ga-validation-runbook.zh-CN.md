# GA 验证手册

发布候选版本将负载和长稳验证与默认 Compose E2E 分开。请在一次性或接近
生产的部署上运行；脚本不会发送凭证或模型提示词。

```sh
AEP_LOAD_BASE_URL=http://localhost:8080 \
AEP_LOAD_DURATION_SECONDS=300 \
AEP_LOAD_CONCURRENCY=16 \
AEP_LOAD_MAX_ERROR_RATE=0.01 \
npm run test:e2e:load
```

默认探测 `/healthz`，并输出包含请求数、失败数、错误率和 p95 延迟的 JSON。
只有测试环境明确通过批准的测试包装器提供请求头时，才应将
`AEP_LOAD_ENDPOINT` 指向只读鉴权接口。错误率超过阈值时命令失败。

也可以手动触发 `AEP GA load validation` 工作流。工作流会启动一次性
Compose 控制服务，等待就绪，将 JSON 结果写入
`artifacts/load-summary.json`，并作为工作流制品上传。请按批准的测试窗口
设置工作流输入；默认运行 300 秒、16 个并发 worker，错误率阈值为 1%。

将输出、部署镜像 digest、数据库和对象存储版本以及测试时间窗口记录为
发布证据。该脚本只是 GA 门禁的一部分，不能替代外部安全评审或备份与灾备
演练。

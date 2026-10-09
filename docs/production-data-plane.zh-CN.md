# 生产数据面部署

`deploy/kubernetes/production` 是 AEP control-service、gateway-authorizer 和 gateway-reconciler 的生产部署基线，目标是已通过 Helm 部署 Higress 的 Kubernetes 集群。`higress-standalone` 仅用于本地 Compose 和 CI 夹具。

## 前置条件

- Kubernetes 对 `aep-system` 强制 restricted Pod Security Standard。
- `higress-system` 中存在已固定版本的生产 Higress Helm Release；在交付系统固定 Chart 与镜像 digest 后，使用 [higress-values.yaml](../deploy/kubernetes/production/higress-values.yaml)。
- cert-manager 提供名为 `production-acme` 的生产 `ClusterIssuer`，或等效证书流程创建 `aep-public-tls`。
- External Secrets Operator 与名为 `production-secrets` 的 `ClusterSecretStore`；将 [external-secrets.yaml](../deploy/kubernetes/production/external-secrets.yaml) 中的示例远程键映射到企业 Secret Manager。
- 托管 PostgreSQL 与兼容 S3 的对象存储；上线前通过经过评审的 overlay 更新 endpoint、issuer、tenant、域名、镜像引用和资源限额。

仓库不会保存 Kubernetes `Secret` 数据、供应商 API Key、签名 seed、Credential keyring 或初始管理员密码。control-service 与 reconciler 都从挂载文件读取共享数据面 token，不会把它放入 Pod 环境变量。

## 部署与验证

先渲染：

```sh
kubectl kustomize deploy/kubernetes/production
```

镜像、域名、Secret Store 映射和外部 endpoint 获批准后，再部署基线：

```sh
kubectl apply -k deploy/kubernetes/production
kubectl -n aep-system rollout status deployment/aep-control-service
kubectl -n aep-system rollout status deployment/aep-gateway-authorizer
kubectl -n aep-system rollout status deployment/aep-gateway-reconciler
```

两个 reconciler 副本各自拥有审计副本目录，并配置了 PDB。两者都使用 field manager `aep-gateway-reconciler` 执行 Kubernetes server-side apply。因为对象名称和内容完全确定，多个副本无需 leader lease 也能安全持有相同写入字段。只有所有线上 Kubernetes 操作均成功后才会上报 `ready`；部分失败会返回 `KUBERNETES_APPLY_FAILED`，并按有界指数退避重试。

reconciler 的 Role 和 RoleBinding 明确限定为 `higress-system` 中的 Ingress、`extensions.higress.io/wasmplugins` 与 `networking.istio.io/envoyfilters`。service-account token 与集群 CA 来自 Kubernetes 投射文件。上线前必须确认已安装 Higress CRD 的组和资源名。

运行 `npm run test:e2e:m3-data-plane` 验证控制面与故障收敛，运行 `npm run test:e2e:m3-kubernetes` 验证真实 Kubernetes API Server 与 Higress 兼容 CRD 门禁。

## AI 用量统计

reconciler 同步下发 `aep-ai-statistics-<deployment-suffix>`，复用官方开源
`ai-statistics` 2.0.1，并将 OCI 制品固定为
`sha256:9bebfc803f6ea92c0805670bd9a6e8a5bb727f2e1a86b133f20bb7002e71511e`。
优先级 200 使统计先于 ai-proxy 执行；`defaultConfigDisable: true` 配合当前部署
已启用的 OpenAI 与 Anthropic Ingress 名称限制作用范围。全部路由禁用后，匹配集
收敛为空；不修改全局插件或其他网关路由。

使用轻量响应属性采集模型和用量元数据，不开启完整问题、回答、工具参数或推理内容
属性。`FAIL_OPEN` 使统计插件无法加载时仍可调用模型。reconciler 的 `ready` 只表示
Kubernetes apply 成功，不表示插件加载或指标采集成功，上线后仍需验证真实指标。
网关必须能获取固定 OCI 制品，或由交付系统镜像该准确制品。

网关在内网 `http://<gateway-pod-ip>:15020/stats/prometheus` 暴露 Prometheus
指标。输入/输出 Token、请求耗时和流式首 Token 耗时复用 Higress 的统计能力，
Token 值依赖上游报告 usage；缺少 usage 不能视为零成本。本阶段不安装
Prometheus/Grafana、不采集请求内容、不添加用户/团队/角色标签、不计算价格，也不
提供持久化请求日志。

本地隔离 Compose 网关使用相同的固定制品验证：

```sh
npm ci
npm run build --workspace @aep/sdk-node
npm run test:e2e:m1-gateway
```

场景检查 OpenAI 非流式/SSE 与 Anthropic 非流式 Mock 响应的真实导出计数器，
验证上游 503 与鉴权行为保持，并检查指标中不包含凭据或模型内容。容器指标端口
保持内网可见。该结果只代表本地验证，不替代生产集群或未测试供应商/协议模式的验收。

回滚到旧 reconciler 时，需显式删除对应部署的
`aep-ai-statistics-<deployment-suffix>` 对象：旧版本不会管理或清除新增插件。
本次没有数据库迁移。

复用集群已有 Prometheus 采集，并将官方面板导入已有 Grafana，详见
[Higress 监控接入](higress-monitoring.zh-CN.md)。

## 目录派生发布

优先使用 `POST /aep/v1/admin/data-plane/publish`，而不是手工编写期望状态。模型目录是单一事实源：管控服务为每个 `sourceType` 为 `gateway`、协议为 OpenAI 兼容或 Anthropic 且 endpoint 与上游模型完整的模型派生一条路由，并原子替换期望状态；禁用模型以 `enabled: false` 路由随行，reconciler 借此删除该路由曾拥有的资源。这从结构上消除了"目录里有模型、网关 WasmPlugin 没有对应 `modelMapping`"的漂移。目录未变化时重复发布是幂等空操作；目录一旦变化就会产生新的按内容寻址的 revision。

凭据映射按约定进行。发布的模型绑定 Credential 时，其路由引用 `higress-system` 下名为 `aep-credential-<credentialId>`、键为 `api-key` 的 Secret。通过部署侧 Secret 系统（例如 External Secret）为每个被引用的 Credential 供给一个这样的 Secret，值为供应商密钥。管控服务不写 Kubernetes，也绝不输出 Credential 明文；reconciler 在每次同步时读取 Secret 并把值内联到渲染出的 WasmPlugin。轮换时先在管控面轮换 Credential，再更新对应 Secret，下一次调和即生效。Secret 缺失的路由渲染时没有 `apiToken`，ai-proxy 会以失败关闭的方式拒绝其请求；anthropic 透传路由的 Secret 缺失时渲染不出凭证头，由上游自己回答 401——失败暴露在上游侧，而不是网关侧失败关闭。

`GET /aep/v1/admin/data-plane/status` 包含 `catalogComparison`：目录可发布但期望路由缺失的模型（`missing`）、目录不会再发布的期望路由（`extra`）、逐字段不一致项（`mismatched`）。比对非空即视为需要评审的漂移；发布可消除漂移，手工 `PUT` 逃生口则用于有意维持的差异（例如下文的原生 `deepseek` provider 类型）。

## DeepSeek 推理路由

路由使用 Higress 原生 DeepSeek provider 时，必须在期望状态中显式设置 `providerType`。未携带该字段的历史路由仍按 `openai` 处理。目录派生的 openai-compatible 路由一律使用 `openai`，因此原生 DeepSeek 路由需要使用手工逃生口，并且在目录获得 provider 类型元数据之前会一直出现在 `mismatched` 中。

~~~json
{
  "revision": "models-2026-08-26",
  "routes": [{
    "modelId": "enterprise-reasoner",
    "enabled": true,
    "endpoint": "/v1/chat",
    "upstreamModel": "deepseek-reasoner",
    "protocol": "openai-compatible",
    "providerType": "deepseek",
    "credentialRef": {"name": "provider-secrets", "key": "deepseek-api-key", "namespace": "higress-system"}
  }]
}
~~~

对应的模型描述应包含 `reasoning` 能力，并通过 `reasoningCompatibility` 声明 `thinkingFormat: deepseek`。客户端必须保留流式和非流式 `reasoning_content`；工具调用会话继续执行时，还必须回放上一条 assistant 消息的 `reasoning_content`。推理请求仍直接走模型网关数据链路，不通过 SDK 控制 API 转发。

## Anthropic 透传路由

`protocol: anthropic` 的目录模型发布为 EnvoyFilter 透传而非 ai-proxy 路由：ai-proxy 的 Claude provider 硬编码 `Host: api.anthropic.com`（智谱 BigModel 这类 Anthropic 协议 CDN 会回答 421），其 OpenAI provider 又会重排请求体。reconciler 按模型渲染、且不触碰任何共享资源（不用 McpBridge）：Ingress `aep-anthropic-<suffix>` 挂在客户端路径前缀 `/<净化后模型ID>` 下，同名 EnvoyFilter 增加一个 STRICT_DNS 上游集群（https endpoint 带 TLS + SNI）、把路由重定向到该集群、改写 Host、把模型前缀替换为 endpoint 自身路径，并在服务端注入 `x-api-key` 与 `authorization` 凭证。

客户端把 anthropic SDK 的 baseURL 指向网关基址加模型前缀（例如 `http://<gateway>/bench-anthropic`），SDK 会追加 `/v1/messages`。请求体逐字节透传：该路径不做服务端模型 ID 改写，请发送 token scope 授权的模型 ID。禁用模型（或删除后重新发布）会在下一次调和时删除整对 Ingress+EnvoyFilter。

**部署顺序很重要**：先滚动 reconciler 镜像、后 control service。旧 reconciler 不认识 `anthropic` 协议，会把此类路由静默按 OpenAI 渲染且上报 `ready`；新 reconciler 配旧 control service 则安全（根本看不到 anthropic 路由）。退役手工维护的透传路由（例如治理仓里 envsubst 渲染的 BigModel 路由）时：注册模型 → 发布 → 经新路由验证 `/<前缀>/v1/messages` → 再删手工 Ingress/EnvoyFilter——两者路径前缀不同，重叠期互不干扰。

## 回滚

仅在旧镜像兼容当前只向前迁移的 schema 时，才使用之前的不可变镜像 digest。通过交付系统回滚 manifests 和 Helm Release；若 schema 兼容性不确定，则从同一协调恢复点恢复 PostgreSQL、对象存储、签名 seed 与 Credential keyring。详细流程见 [production-runtime.zh-CN.md](production-runtime.zh-CN.md)。

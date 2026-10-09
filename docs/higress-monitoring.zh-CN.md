# 对接已有 Prometheus / Grafana

本阶段复用集群已有监控系统。第一阶段的 reconciler 先下发开源
`ai-statistics`，本阶段再采集 Higress 内网 `15020/stats/prometheus`。
不新增生产 Prometheus、Grafana、数据库或公网指标入口。

指标历史保存在已有 Prometheus 的 TSDB / remote-write 后端；保留期、磁盘、备份和
HA 去重沿用该系统的策略。Grafana 负责查询展示。此处不提供逐请求明细、成本计价或
团队/角色/用户归因，缺失的 usage 也不应显示为零成本。

## 选择一种采集方式

先检查服务器已有采集任务。若网关每个 Pod 已通过注解、ServiceMonitor 或 PodMonitor
被采集，优先复用该任务，只检查指标是否出现；不要再增加第二个任务。不要把
负载均衡 Service 当成单一 target，否则多副本的计数器可能交错或漏采。

### 已安装 Prometheus Operator

使用 Higress 官方 Chart 内置 PodMonitor，参考
[可选 values](../deploy/kubernetes/monitoring/higress-values.yaml)。它独立于 AEP
生产 kustomization，且关闭 Chart 自带监控组件和 Pod 注解采集。

先从服务器读取真实配置，而不是假设 Helm release 标签为 `kube-prome`：

```sh
kubectl get prometheus -A
kubectl -n <监控命名空间> get prometheus <实例名> -o yaml
kubectl -n higress-system get pods --show-labels
```

在私有部署 values 中配置 `higress-core.gateway.metrics.podMonitorSelector` 标签，
使其匹配该 Prometheus 的 `spec.podMonitorSelector`。这里 Chart 名为
`podMonitorSelector` 的 values 实际是 PodMonitor 的 **metadata.labels**。
仓库 overlay 仅清除上游默认 `release: kube-prome`，没有猜测服务器标签。
如果真实选择器包含 `release`，由私有 values 写入真实值；如果使用其他键，确认最终
渲染中没有残留的 `release` 标签。还需确认：

- `spec.podMonitorNamespaceSelector` 包含 `higress-system`。空和缺省选择器含义不同，
  按实际 Operator 版本核对；需要调整时仅开放所需命名空间。
- Prometheus/Operator 的 RBAC 可发现该命名空间的 PodMonitor 和 Pod。
- 若监控或 Higress 命名空间有 NetworkPolicy，允许实际 Prometheus Pod 到网关 Pod
  TCP 15020 的出入站流量。无需开放公网或给 AEP control-service 增加查询权限。

在部署系统用已固定的 Chart、release 名称和完整现有 values **先渲染后评审**：

```sh
helm template <已有-release> <已固定-chart.tgz> --namespace higress-system \
  -f deploy/kubernetes/production/higress-values.yaml \
  -f deploy/kubernetes/monitoring/higress-values.yaml \
  -f <完整私有部署-values.yaml> > <待审查清单.yaml>
```

本地已验证官方 Higress 2.2.4 Chart，下载地址为
`https://higress.io/helm-charts/higress-2.2.4.tgz`，SHA-256 为
`c048d97805f7925d74051674e1f639fa178e5b7ce4eabd893d03b02eff653787`。
这不是服务器当前版本的判断；其他版本应重新验证模板。升级现有 release 时保留所有
原有私有配置，先确认 diff 未删除已有监控组件、PVC 或业务配置。若已有 Higress 自带
监控正在使用，不应直接用这个 overlay 关闭它；需先完成迁移评审。

生成的 PodMonitor 只选择本 release 的网关 Pod，端点名为 `istio-prom`、端口为
15020、路径为 `/stats/prometheus`，间隔 30 秒、超时 10 秒。`higress` Pod 标签被
显式保留到指标上，供官方 Grafana 面板的网关筛选器使用。

### 未使用 Operator

将 [普通 Prometheus job 示例](../deploy/kubernetes/monitoring/prometheus-scrape.example.yaml)
合入已有 `scrape_configs`，根据实际 Pod 标签调整 app/higress 筛选，给已有采集账号
授予 `higress-system` 中 Pod 的 get/list/watch。只保留 `istio-prom` 端口，避免同一 Pod
其他端口产生重复 target。用已有服务器相同版本的 `promtool check config` 验证完整
合并配置，然后按原有变更流程 reload。此方式不应用 PodMonitor overlay，并应消除与
既有注解任务的重复采集。

## Grafana 复用官方 AI 面板

在已有 Grafana 中使用指向上述 Prometheus 的现有 datasource，取得它的 UID，运行：

```sh
node scripts/render-higress-dashboard.mjs \
  --datasource-uid <已有-Prometheus-datasource-UID> \
  --output <本地目录>/aep-higress-ai.json
```

脚本从 Higress Console v2.2.4 的固定提交下载官方 `ai.json` 并校验 SHA-256，然后仅
替换 datasource UID、面板名称/UID，并为原先未限定范围的 AI 查询加入
`higress="$gateway"` 筛选。保留官方面板和公式，生成文件需通过已有 Grafana 的
Import 流程导入；脚本不连接或修改 Grafana。可用 `--template <缓存的官方-ai.json>`
离线生成，校验同样生效。

在面板选择实际 gateway。官方面板中 WAF、consumer 等区域依赖其他插件或标签，
本阶段未启用它们；`ai_consumer` 也不等于 AEP 用户/团队/角色。

## 验收与排障

在 Prometheus Targets 中，每个网关 Pod 对应一个健康 target。避免同一 Prometheus
内重复任务；HA 副本跨集群查询时按已有平台规则去重。在 Grafana Explore 中验证：

```promql
up{job="higress-gateway"}
sum by (ai_route, ai_model) (rate(route_upstream_model_consumer_metric_input_token{higress="higress-system-higress-gateway"}[5m]))
sum by (ai_route, ai_model) (rate(route_upstream_model_consumer_metric_output_token{higress="higress-system-higress-gateway"}[5m]))
sum by (ai_route, ai_model) (rate(route_upstream_model_consumer_metric_llm_failure_count{higress="higress-system-higress-gateway"}[5m]))
```

`higress` / `job` 按实际标签替换。曲线需要至少两个采样点；新时间序列在首次采集前
发生的请求可能无法被 `increase` 还原。插件仅观测到达 Higress 的调用，authorizer
提前拒绝的 401/403 不在这些 AI 指标中。Token 依赖上游 usage；无数据先检查
Wasm 加载、真实调用、原始 endpoint，再检查 target 和选择器，不要用零值掩盖故障。

本地验证：

```sh
npm ci
npm run build --workspace @aep/sdk-node
npm run test:e2e:m1-gateway
```

E2E 使用独立 Compose 项目和固定 digest 的临时 Prometheus，实际抓取真实 Higress，
验证 OpenAI 非流式/SSE、Anthropic 非流式 Token、上游失败计数以及历史区间查询；
结束后清理临时容器和数据。它验证采集/查询链路，不替代服务器的 Operator 选择器、
NetworkPolicy、持久化保留期或 Grafana UI 验收。

回滚时恢复已有 release 的 values / scrape job，移除本阶段新增的 PodMonitor 或
job，恢复原注解策略，避免双重采集；按需删除导入的面板。不删除已有 Prometheus
历史数据。第一阶段插件回滚单独遵循[数据面说明](production-data-plane.zh-CN.md)。

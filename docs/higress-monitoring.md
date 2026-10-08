# Reuse Existing Prometheus / Grafana

This stage reuses the cluster's monitoring stack. Deploy the first-stage native
`ai-statistics` plugin before collecting Higress's internal
`15020/stats/prometheus` endpoint. No production Prometheus, Grafana, database,
or public metrics endpoint is added.

Historical metrics live in the existing Prometheus TSDB / remote-write backend.
Retention, disk, backup, and HA deduplication follow that system's policies;
Grafana queries and displays the data. Per-request records, pricing, and AEP
team/role/user attribution are outside this integration. Missing usage is not
zero cost.

## Choose One Scrape Path

Inspect existing targets first. If each gateway Pod is already scraped through
annotations, a ServiceMonitor, or a PodMonitor, reuse that job and check for the
new metrics instead of adding another. Do not scrape a load-balanced Service as
one target: counters from different replicas may interleave or be missed.

### Prometheus Operator

Use the official Higress Chart's native PodMonitor with the
[opt-in values](../deploy/kubernetes/monitoring/higress-values.yaml). This overlay
is separate from the AEP production kustomization and disables the bundled
monitoring stack and annotation-based Pod scraping.

Read the actual configuration before choosing selector labels:

```sh
kubectl get prometheus -A
kubectl -n <monitoring-namespace> get prometheus <name> -o yaml
kubectl -n higress-system get pods --show-labels
```

In private deployment values, set
`higress-core.gateway.metrics.podMonitorSelector` to labels matching the existing
Prometheus's `spec.podMonitorSelector`. Despite its name, this Chart value sets
the PodMonitor's **metadata.labels**. The repository overlay only clears the
upstream `release: kube-prome` default; it does not guess server labels. If the
live selector uses `release`, supply its real value privately. If it uses other
keys, verify that no default `release` label remains in the final render. Also
check:

- `spec.podMonitorNamespaceSelector` includes `higress-system`. Absent and empty
  selectors have different semantics; verify the installed Operator's behavior
  and open only the required namespace if a change is needed.
- Prometheus/Operator RBAC permits discovery of PodMonitors and Pods there.
- Any monitoring/Higress NetworkPolicies permit egress and ingress from the real
  Prometheus Pods to gateway Pods on TCP 15020. No public exposure or AEP
  control-service query permission is required.

Render with the pinned Chart, existing release name, and complete private values
in the deployment system before review:

```sh
helm template <existing-release> <pinned-chart.tgz> --namespace higress-system \
  -f deploy/kubernetes/production/higress-values.yaml \
  -f deploy/kubernetes/monitoring/higress-values.yaml \
  -f <complete-private-values.yaml> > <review-manifest.yaml>
```

Local rendering used official Higress 2.2.4 from
`https://higress.io/helm-charts/higress-2.2.4.tgz`, SHA-256
`c048d97805f7925d74051674e1f639fa178e5b7ce4eabd893d03b02eff653787`.
This does not identify the server's installed version; validate other versions
separately. Preserve all existing private configuration when upgrading the
release, and inspect the diff for removed monitoring resources, PVCs, or business
configuration. If Higress's bundled monitoring is already in use, review its
migration before applying an overlay that disables it.

The rendered PodMonitor selects only this release's gateway Pods. Its endpoint
uses the named `istio-prom` port (15020), `/stats/prometheus`, a 30-second
interval, and a 10-second timeout. The `higress` Pod label is explicitly retained
as a target label for the official Grafana dashboard's gateway selector.

### Prometheus Without Operator

Merge the [plain scrape job example](../deploy/kubernetes/monitoring/prometheus-scrape.example.yaml)
into the existing server's `scrape_configs`. Match app/higress to the actual
gateway Pod labels and grant its existing service account get/list/watch on
Pods in `higress-system`. Keep only the named `istio-prom` port to avoid targets
for other ports on the same Pod. Validate the complete merged configuration with
the server's matching `promtool check config`, then reload through the existing
change process. Do not apply the PodMonitor overlay for this path, and eliminate
overlap with existing annotation jobs.

## Import the Official AI Dashboard

Use the existing Grafana Prometheus datasource pointing at that collector. Obtain
its UID, then run:

```sh
node scripts/render-higress-dashboard.mjs \
  --datasource-uid <existing-prometheus-datasource-uid> \
  --output <local-directory>/aep-higress-ai.json
```

The script downloads official Higress Console v2.2.4 `ai.json` from an immutable
commit and verifies its SHA-256. It changes only datasource UIDs, dashboard
name/UID, and previously unscoped AI selectors to include `higress="$gateway"`.
Official panels and formulas remain intact. Import the generated JSON through
the existing Grafana workflow; the script does not connect to or modify Grafana.
For offline generation, pass `--template <cached-official-ai.json>`; the same
checksum validation applies.

Choose the actual gateway in the dashboard. WAF/consumer sections depend on
other plugins or labels not enabled here; `ai_consumer` is not an AEP
user/team/role identity.

## Acceptance and Troubleshooting

Prometheus Targets should show one healthy target per gateway Pod. Avoid duplicate
jobs within one Prometheus; deduplicate HA replicas in shared queries according
to the existing platform's rules. In Grafana Explore, check:

```promql
up{job="higress-gateway"}
sum by (ai_route, ai_model) (rate(route_upstream_model_consumer_metric_input_token{higress="higress-system-higress-gateway"}[5m]))
sum by (ai_route, ai_model) (rate(route_upstream_model_consumer_metric_output_token{higress="higress-system-higress-gateway"}[5m]))
sum by (ai_route, ai_model) (rate(route_upstream_model_consumer_metric_llm_failure_count{higress="higress-system-higress-gateway"}[5m]))
```

Replace `higress` / `job` with the live labels. Curves need at least two samples;
`increase` may miss requests before a series' first scrape. These AI metrics see
only requests reaching Higress: 401/403 rejected by the authorizer are excluded.
Token values depend on upstream usage. For missing data, check Wasm loading, a
real call, and the raw endpoint before checking targets/selectors. Do not hide a
collection failure by substituting zero.

Local verification:

```sh
npm ci
npm run build --workspace @aep/sdk-node
npm run test:e2e:m1-gateway
```

The isolated Compose E2E uses a temporary digest-pinned Prometheus to scrape real
Higress and query stored OpenAI non-streaming/SSE and Anthropic non-streaming
Token counters, upstream failures, and a historical range. It removes its
containers/data afterward. This proves the scrape/query path, not the server's
Operator selectors, NetworkPolicy, persistence retention, or Grafana UI.

To roll back, restore the existing release values / scrape job, remove the added
PodMonitor or job, and restore the previous annotation policy without duplicate
scraping. Remove the imported dashboard if needed; do not delete Prometheus
history. Roll back the first-stage plugin separately using the
[data-plane runbook](production-data-plane.md).

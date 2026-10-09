# Native gateway management APIs

## Metric definitions

Every metric response includes `source` and a required `definition` with `id`,
`unit`, `aggregation`, `windowSeconds`, `groupBy`, and `modelDimension`. These
describe the native query; they do not calculate telemetry. The SDK preserves
both native values and metadata. Clients must label definitions explicitly and
must not combine different definitions or sum returned points into totals.

| Metric | Prometheus definition | Loki definition |
| --- | --- | --- |
| calls | `ai_usage_completed_calls`: completed requests with plugin usage/duration records | `gateway_access_requests`: gateway access-log requests |
| failures | `ai_detected_failures`: plugin-detected model response failures | `gateway_http_errors`: gateway log HTTP status >=400 |
| input_tokens / output_tokens | `ai_input_tokens` / `ai_output_tokens`: reported usage counter increases | `gateway_log_input_tokens` / `gateway_log_output_tokens`: native range sums of logged usage |
| first_token_duration / service_duration | `ai_usage_mean_first_token_duration` / `ai_usage_mean_service_duration`: duration/count rate means | `gateway_log_mean_first_token_duration` / `gateway_log_mean_service_duration`: means of logs carrying the duration field |

QPS uses `envoy_downstream_qps` / `envoy_upstream_qps`. The existing success_rate
names mean `envoy_downstream_non_5xx_ratio` / `envoy_upstream_non_5xx_ratio`, in
0–1 ratio units; they are not model business success rates.
`authorizer_http_requests` counts authorizer HTTP requests grouped by status.

Catalog `modelId` and organization queries use Loki. Prometheus model groups
identify upstream models; Loki identifies AEP catalog models. `modelDimension`
exposes that distinction. Pin a previously returned `expectedDefinition` to its
`definition.id` when keeping the same semantics. A changed selected definition
returns 422 `GATEWAY_METRIC_DEFINITION_MISMATCH` before any source request.

`windowSeconds` is each point's native lookback: 120 seconds for rate queries,
otherwise `step` (default 60). `step` controls output sampling and may differ
from the lookback. Counter series are not whole-period totals. Missing usage,
empty series and unknown values are not zero-filled.

These are AEP adapters: Higress/ai-statistics already exports observations,
Prometheus/Loki provides queries, Console manages plugin configuration and
ai-quota exposes balance management. No new numeric or inference engine is added.

The administrator surface under `/aep/v1/admin/model-gateway` connects native
Higress, Prometheus and Loki results to AEP session authorization. Monitoring
results are returned with their source; neither the SDK nor AEP calculates
usage, percentages, organization totals, latency percentiles or costs.

Read `capabilities` before enabling controls. Disabled sources return 503
`GATEWAY_SOURCE_UNAVAILABLE`; dimensions not provided by the native source
return 422 `GATEWAY_DIMENSION_UNSUPPORTED`. No missing usage is converted to
zero. P95/P99, costs, and the session dimension are excluded.

- `GET capabilities`, `GET health`: source configuration and native health.
- `GET metrics`: bounded native queries with `metric`, RFC3339 `start`/`end`,
  optional `step`, `groupBy`, `modelId`, `userId`, `teamId`, `roleId`.
- `GET requests`, `GET requests/{requestId}`: bounded native log queries;
  deployment context always comes from the authenticated session. A numeric
  native timestamp cursor makes backward pagination exclusive. The source
  must capture trusted metadata rather than prompts, responses or tokens.
- `GET limits`, `GET/PUT/DELETE limits/{ruleId}`: versioned native plugin rule
  configuration. New rules use `expectedVersion: 0`; updates/deletes require
  the observed version. Draft changes do not become active until publication.
- `POST limits/publish`, `GET limits/status`: publication and reconciler state;
  Kubernetes apply success is not proof of runtime enforcement.
- `GET quotas/{userId}`, `POST quotas/{userId}/refresh`,
  `POST quotas/{userId}/delta`: delegate to native ai-quota. The native balance
  is authoritative. Delta operations are not automatically retried.
- `POST models/{modelId}/test-access`: obtain short-lived access for one enabled,
  assigned model and the existing authenticated session. Inference uses the
  existing direct authorizer → Higress → provider path. No control-API inference
  proxy, custom model executor or provider credential is introduced.

The protocol version header remains `X-AEP-Protocol-Version: 1.0`. Existing
model catalog, assignment, health, identity, events and data-plane APIs remain
the source for administrative metadata. The new contracts do not imply that an
unconfigured deployment already has a log store or native quota plugin.

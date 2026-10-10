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

For `health`, a dedicated Prometheus source bound to the authenticated deployment
with `AEP_GATEWAY_METRICS_DEPLOYMENT` can report unlabeled scrape targets from
the repository discovery job or PodMonitor. Shared sources must label targets
with a matching `aep_deployment_id`. An explicit foreign label, or a source bound
to another deployment, is excluded. Only native health, last scrape time and
duration are returned; addresses, labels and free-text scrape errors stay private.

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
# Native source configuration

Set `AEP_GATEWAY_PROMETHEUS_URL` and/or `AEP_GATEWAY_LOKI_URL` to the existing
monitoring services; optional `*_TOKEN` and `*_TOKEN_FILE` supply their own
service credentials. Browser access tokens are never forwarded. HTTP clients
reject redirects, bound response size to 4 MiB and time out after 8 seconds.
The sources receive the authenticated deployment as `X-Scope-OrgID`.

AI metrics additionally constrain `ai_consumer` to the authorizer's encoded
deployment/user identity. They expose the native `ai_model` label (the upstream
model), not a reconstructed catalog name. `calls` is the native usage-bearing
`llm_duration_count`, not a claim that authorizer rejections contain token usage.
Historical `none` consumers cannot be safely assigned to users retroactively.

Envoy connection-manager/cluster counters lack consumer labels. Enable their
QPS/non-5xx ratios and authorizer status counts only with
`AEP_GATEWAY_METRICS_DEPLOYMENT=<deployment-id>` **and a dedicated datasource
containing only that deployment's inference gateway and authorizer metrics**.
For a shared datasource these operations return 422. Native non-5xx ratios do
not mean model business success. Stock Prometheus plugin labels lack organizations.
With `AEP_GATEWAY_ORGANIZATION_LOGS=true` and the metadata-only Vector/Loki
pipeline from the identity stage, organization queries use **native Loki**
count/sum range functions over call-time membership streams. The collector
only copies metadata into membership streams; it calculates no numbers. Base
streams are used for filtered totals, membership streams for team/role groups,
so the overall total is never the sum of overlapping memberships. Native
access-log failures mean HTTP status >=400, unlike ai-statistics model-body
failure counts. Neither source is silently substituted for the other.

Loki streams must use `aep_deployment_id` and `aep_source=gateway|authorizer`.
Only the allowlisted access-log fields described by the deployment guide may
be ingested into these streams. The query renders only request/model/user,
membership, status/flags, tokens and native duration fields; prompts,
responses, tool arguments, credentials and free-form provider errors are not
returned. AEP stores no copy of the monitoring history.

## Active model tests

`POST models/{modelId}/test-access` requires `models.read`, an active session,
and a current model assignment. Administrator status does not bypass model
assignment. Only enabled gateway models are accepted; callers cannot supply
an endpoint. The response contains the deployment-configured gateway URL,
protocol path, and a single-model credential valid for at most two minutes.
Production reuses the registered, non-revoked License with `enterprise.models`
and the existing entitlement protocol. License expiry/grace bounds the test
expiry as well. Development may use the existing model JWT protocol.

The standalone browser Admin Console calls authorizer → Higress → provider
directly, discards credentials after testing, and must not log or persist them.
The Electron renderer's existing main-process credential boundary is unchanged.
OpenAI tests use `baseUrl + /chat/completions`; Anthropic tests use the returned
model path `baseUrl + /v1/messages`. AEP neither generates model responses nor
counts tokens. This endpoint issues restricted credentials for the existing
inference path; it is not a new Higress-native testing plugin.

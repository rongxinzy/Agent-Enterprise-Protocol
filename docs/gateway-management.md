# Native gateway management APIs

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

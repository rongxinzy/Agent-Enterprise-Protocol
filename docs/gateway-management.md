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

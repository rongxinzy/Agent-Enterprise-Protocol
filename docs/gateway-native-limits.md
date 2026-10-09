# Native limits and quota deployment

Rules are versioned configuration stored in AEP PostgreSQL, not usage data.
Writes/deletes require optimistic versions; publication takes a deployment
advisory transaction lock and snapshots every rule, including disabled
tombstones. Edits remain drafts until `POST .../limits/publish`. There are at
most 100 rule identities per deployment; reuse disabled rules for policy
changes. Deleted identities remain tombstones to ensure deactivation and
cannot silently reset or evade native Redis counters.

Enable the authorizer's call-time identity endpoint from the identity guide.
It emits a bounded trusted `X-AEP-Limit-Keys` header for user/model/team/role
memberships and combinations. Each published rule becomes a separate pinned
`cluster-key-rate-limit` or `ai-token-ratelimit` WasmPlugin. All enabled rules
can therefore act independently regardless of an artifact's first-match or
all-match behavior within `rule_items`. Request thresholds use `query_per_*`;
token thresholds use the native `token_per_*`. There is no AEP counter, local
tokenizer or custom rate-limit engine.

Mount an operator-owned configuration file into the reconciler and set
`AEP_RECONCILER_NATIVE_GATEWAY_CONFIG_FILE` to its path. Start with
`deploy/kubernetes/monitoring/gateway-native-config.example.json`. Redis is an
additional native plugin dependency; Prometheus/Grafana cannot serve as its
counter store. Provision the Redis service and named Kubernetes Secrets in
`higress-system` through the existing Secret system. Credentials are resolved
only inside the reconciler, never returned in administrative APIs. The
example is opt-in and does not deploy a database or change production values.

The reconciler fetches published rules via its existing internal service token
and applies only deployment-owned resources. Disabled/deleted rules produce
empty match rules, leaving model routes intact. Publication status reports
`pending`, `applied`, or `error`. `applied` confirms Kubernetes apply and a
matching revision acknowledgment; `runtimeVerified` stays false because that
does not prove the plugin is loaded, Redis is reachable, or a call is rejected.

Supplying `quotaAdminCredentialRef` also provisions native ai-quota and a
dedicated key-auth management ingress. Native management paths include
`/v1/chat/completions/quota` (required by ai-quota), under
`/aep-quota-<deployment-resource-suffix>/`. Configure the control service with
`AEP_GATEWAY_QUOTA_URL=http://<private-higress>/aep-quota-<suffix>/v1/chat/completions/quota`
and `AEP_GATEWAY_QUOTA_TOKEN_FILE` pointing to the same admin token Secret.
The generated native key-auth credential is the corresponding `Bearer` header.
Do not expose this service credential to browsers or end users.

Quota APIs validate that the user belongs to the authenticated deployment,
derive its encoded Consumer server-side, and issue the native GET/refresh/delta
operation. Native mutations return plain text; AEP then reads the native JSON
balance instead of calculating a new value. A timeout or failed read after a
delta leaves its result uncertain: never automatically retry it. Native
quota/token limits inherit the plugin's request-completion accounting and
concurrency semantics; they are not a transactional billing ledger.

OCI digests for request limits, token limits, ai-quota and key-auth are pinned
in the renderer. Validate the actual artifacts on a disposable deployment
before enabling enforcement on production traffic.

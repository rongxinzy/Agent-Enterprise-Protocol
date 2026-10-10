# Native limits and quota deployment

Rules are versioned configuration stored in AEP PostgreSQL, not usage data.
Writes/deletes require optimistic versions; publication takes a deployment
advisory transaction lock and snapshots every rule, including disabled
tombstones. Edits remain drafts until `POST .../limits/publish`. There are at
most 100 rule identities per deployment; reuse disabled rules for policy
changes. Deleted identities remain tombstones to ensure deactivation and
cannot silently reset or evade native Redis counters.

Enable the authorizer's call-time identity endpoint from the identity guide.
It emits a trusted `X-AEP-Limit-<SHA256>` presence header with constant value `1`
for each user/model/team/role subject and model-bound combination. Native
`limit_by_header` uses exact matches. Shared team counters therefore do not
split by user, model or unrelated memberships. Up to 80 subject headers (38
distinct team/role memberships) are allowed per call; excess membership fails
closed, reserving room below Envoy's default 100-header limit for standard
headers. Each published rule becomes a separate pinned
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

Token limits and quota use the official Higress plugin **2.0.3** artifacts from
the [2.2.5 snapshot](https://github.com/higress-group/higress/blob/2ba624cc479dd28bb88a853d6889e7931b993537/plugins/release/snapshots/2.2.5.json),
built from source `2b837c0ada8dbfb3e4bd92fc3f18ea532d269cb1`:

| Plugin | OCI manifest digest |
| --- | --- |
| ai-token-ratelimit | `sha256:9276a7d4cbd7afef668fd1aaf41212e663fd7fa98661c619398a7f2fb2736679` |
| ai-quota | `sha256:2684810410de2803200f21d4971fe30161a7c0060c39ccab3906f6bcd4b8a509` |

The previous June 2025 token artifact rejected `global_threshold` with
`missing rule_items`, causing fail-closed HTTP 500. The previous quota artifact
ignored Anthropic paths and usage, allowing calls even with zero quota. The
new quota artifact includes the upstream non-streaming deduction fix as well
as native Anthropic path and usage support. AEP still delegates all checks and
accounting to the plugins and Redis.

Run `npm run test:e2e:gateway-native` to exercise the production renderer with
real Higress 2.2.4 and Redis, mock OpenAI/Anthropic providers, JSON/SSE quota
deduction and zero-balance denial, shared global/team Token limits, disabled
rules and Redis outages. The command is included in the standard Compose E2E
gate. It adapts only standalone fixture routes and service discovery; it does
not validate Kubernetes Pod networking or a production monitoring deployment.

The newer Token plugin uses an accumulated counter and a different Redis key
layout from the old remaining-allowance artifact. Existing rate-limit windows
are not carried over when upgrading: publish during an operator-controlled
window. Quota balances keep the same `aep_quota:` prefix and encoded Consumer;
no AEP balance migration or arithmetic is introduced. Runtime status remains
`runtimeVerified: false` until the actual deployment is accepted.

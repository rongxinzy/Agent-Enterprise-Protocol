# Native model access projection

[简体中文](model-access-operator.zh-CN.md) | English

This opt-in stage of [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338)
adds an actual controller to the existing Kubebuilder Operator. It consumes
admitted [ModelAccessGrant](model-access-grants.md) session bindings and writes
owned Authorino v0.26.4 `AuthConfig` and freshness ConfigMap resources. It does
not replace the default authorizer, connect shared Higress ingress, implement
workload authentication or establish complete product acceptance. Adoption remains gated.

## Ownership and configuration

DB authorization remains authoritative. Use the existing transaction outbox and
publication relay; this controller has no DB or Secret access of its own and
adds no alternate publisher. Grants must carry the exact binding name, relay
markers, deployment label and current DeploymentConfig identity. Human and
agent **session** bindings are supported; Kubernetes workload bindings fail
closed pending a separate trusted-verifier stage.

Both the default deployment and its RBAC remain unchanged. Explicitly rendering
`kubectl kustomize services/gateway-operator/config/access-rbac` produces only
an additional `aep-system` Role and RoleBinding for the existing Operator service
account. It permits reading grants/deployment config, updating grant status and
creating/updating/watching ConfigMaps and AuthConfigs. It grants no Secrets,
TokenReview, global binding or source deletion permissions. Existing controllers
retain their existing separately scoped permissions. Exclusive deployment
ownership is still required; field managers and owner references alone cannot
prevent another privileged writer.

Enabling the controller requires all of:

| Variable | Meaning |
| --- | --- |
| `AEP_GATEWAY_ACCESS_OPERATOR_ENABLED=true` | Explicit opt-in; otherwise no access controller is registered |
| `AEP_GATEWAY_ACCESS_ISSUER_URL` | Trusted AEP JWT discovery issuer |
| `AEP_GATEWAY_ACCESS_POLICY_BASE_URL` | Trusted static service for projected Rego files |
| `AEP_GATEWAY_ACCESS_REFRESH_SECONDS` | Explicit native refresh interval, 1–60 seconds; not a production default |
| `AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT` | Defaults to true; false additionally permits model JWTs |

Invalid booleans, intervals and URLs fail startup. URLs must be HTTP(S), without
userinfo, query or fragment. HTTP allowance supports isolated tests and is not
a production transport-security policy. The policy endpoint can change
authorization bounds: restrict its writers and network callers and use a
reviewed TLS/NetworkPolicy deployment. No such production static delivery
service or overlay is delivered here. The independently reviewed
[HTTPS source in #357](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/357)
supplies this component. The Node file source is a disposable test fixture and
must never be deployed as that service.

## Stable policy and changing freshness

Each session binding owns a stable AuthConfig plus one ConfigMap. The Operator
hashes the actual semantic projection and trusted options independently of the
source publication hash. Role/team/model/license/configuration changes alter
that semantic identity; issuance, validity and publication markers do not.
Models, role/team IDs and licenses are sorted before hashing.

Native authentication caches the verified AEP JWT identity. Native OPA caches
the complete session decision and allowed model range. An **uncached** native
OPA boundary checks the semantic hash, actual JSON `body.model`, token scopes,
numeric time claims, publication absolute deadline and exact license ID/digest
and grace deadline on every request. These checks still execute on warm results;
there is no per-request business DB query or custom authorization cache. This
stage covers JSON-body model requests only, not multipart or model-from-path
requests introduced by the multimodal gateway work.

Renewal updates only the ConfigMap, never AuthConfig metadata/spec/status. This
matters because an upstream metadata event can rebuild evaluator caches. The
ConfigMap is written before AuthConfig, so mixed semantic generations deny on
hash mismatch. Sequence/owner guards, live parent checks, resourceVersion
conflicts and read-back reject replay and foreign ownership. Kubernetes has no
cross-object transaction: a parent change can still occur between a check and
a child write. Watch retries and the last published absolute deadline bound
that convergence; these checks do not promise atomic revocation.

Authorino refreshes each external module by polling per evaluator/process; it
does not watch the ConfigMap itself. Download/compile failures retain the last
good module until its absolute deadline denies. ConfigMap-volume projection
adds a separate propagation delay. Deleting a grant uses same-namespace owner
GC; GC and source interruption cannot extend the last compiled deadline.

`Applied=True` means validated native-resource read-back. `Ready` stays false
and replica observations are cleared: API writes, Pod readiness, native
AuthConfig status and Lease leadership do not establish adoption by every
serving process. The internal `x-aep-access-observation` response contains only
semantic hash/publication evidence. It is not a cache-hit witness, a trusted
subject header or an external API; a later ingress integration must strip
caller identity hints and keep this evidence internal.

## Verification and remaining gates

Unit/API tests cover ownership, source replay, same-version file alteration,
unexpected binary data, expiry, status isolation, option parsing and opt-in
namespace permissions. With `KUBEBUILDER_ASSETS`, real API-server tests run in
CI using schema-neutral native storage. Setting `AEP_AUTHORINO_CRD_PATH` to the
fixed v0.26.4 CRD additionally checks its SHA-256 and real admission defaults;
schema-neutral storage alone is not native-admission evidence.

The explicit probe builds the current Operator with ko into its own disposable
Kind cluster, runs two Operator and two fixed Authorino replicas, and consumes
actual AEP issuer/discovery/JWKS output. It checks allow/deny, invalid JWTs,
actual-body model escalation, renewal without AuthConfig writes and disabled
revocation. It installs a Higress schema fixture, **not** a live Higress gateway,
business DB, provider or employee runtime. Kind is only the local test tool.

```powershell
$env:KIND = 'path/to/kind'
$env:KO = 'path/to/ko'
$env:AEP_AUTHORINO_CRD_PATH = 'path/to/pinned/authconfigs.yaml'
$env:AEP_NATIVE_ACCESS_REPORT = 'path/outside/repository/report.json'
$env:AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT = 'true'
node tests/e2e/native-access-projection.mjs
```

Docker must already have `node:24-alpine`, the pinned Authorino v0.26.4 tag and
the fixed Kind node image used by the script. The Authorino archive retains its
original OCI index/digest; single-platform export can rewrite the index and
leave dangling containerd import references. Reports never contain credentials.

For an explicit integration with #357's actual dual-replica HTTPS manifests,
also set `AEP_NATIVE_ACCESS_POLICY_SOURCE_DIR` to its
`deploy/kubernetes/access-policy-source` directory and `OPENSSL` to an OpenSSL
executable. The test creates disposable PKI outside the repository and appends
its CA to the pinned Authorino image's full public trust bundle. Two authorized
processes consume the source; a third cold negative control lacks that private
CA and must deny specifically on x509 verification. Source interruption,
absolute expiry, offline renewal denial and recovery are checked without
AuthConfig rewrites. This is still CR-based component integration, not a
business DB/Higress/employee acceptance or NetworkPolicy enforcement test.
Private files and the owned cluster are cleaned in `finally`.

### Business mode starting from real management APIs

With the HTTPS parameters above, add these settings and run the same entry:

```powershell
npm run build --workspace @aep/sdk-node
$env:AEP_NATIVE_ACCESS_BUSINESS = 'true'
$env:AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT = 'false'
node tests/e2e/native-access-projection.mjs
```

Docker must also cache `postgres:17.6-alpine` and `rustfs/rustfs:1.0.0`. The
probe builds the current Control Service with ko and deploys disposable
PostgreSQL, object storage and the real server in its owned Kind cluster.
Random administrator/storage passwords, signing seed and Credential key enter
runtime Secrets only, never images, repository files or reports. The fixture
retains restricted Pod Security and never accesses an existing local or
production database.

Business mutations use the SDK/public management API exclusively: models are
assigned only to roles, inherited by users directly or through team default
roles. Checks cover team-default, direct-role and team-membership changes,
role/team disable/restore, role model-assignment deletion/recreation, individual
session revocation,
account disable/re-enable, model disable/restore, actual Control/relay outage
expiry/recovery and rediscovery after an authorization process replacement.
Read-only SQL correlates the latest DB publication's operation ID, sequence,
hash and phase with the CR and process observations. Neither SQL writes nor
manual CR edits simulate authorization changes.

The acceptance observer independently enumerates the two current Ready Pods,
addresses each Pod directly, captures UID/containerID/restart count and checks
the instance set before and after requests. This is test instrumentation, not
a production all-process observer, and never sets `Ready=True`. Internal
execution responses are still not cache-hit witnesses. Business mode uses real
service-issued human-session model JWTs; enterprise entitlements, trusted
employee workloads, shared Higress ingress, provider inference, quota and
NetworkPolicy enforcement remain outside its scope. `adoptionGate` stays false.

Remaining adoption gates include:

- Broader DB→outbox→relay→CR→Operator business coverage, including licenses,
  scheduled expiry and agent bindings; a production all-serving-process
  discovery/adoption observer and safe shared Higress ingress. The human-session
  probe above does not complete these gates.
- Trusted workload binding, existing Consumer/Redis quota identity and
  multimodal request extraction. No new quota accounting is supplied here.
- Secure dynamic static-file delivery, watch interruption, expiry/recovery and
  configuration rollout. A software/options change can alter a rendered file
  at the same source sequence and is intentionally rejected: stop the old
  writer and obtain a newer verified publication before changing render semantics.
  Do not mix configurations between controller replicas or mutate old markers.
- Capacity, concurrency and eviction. Native caches allocate memory per
  evaluator per binding. freecache rejects entries larger than 1/1024 of the
  store; raw enterprise JWT keys plus identity claims exceeded the upstream
  1 MB default in this probe. Its 4 MB stores cover only the small test fixture,
  not maximum scopes or deployment scale. The one-year retention ceiling is
  not credential validity: uncached token/publication/license deadlines still
  apply. Storage/retention and high-cardinality costs require measured approval.

The [component renewal evidence](auth-result-cache-feasibility.md) establishes
the native mechanism independently; it is not a substitute for these remaining
product gates. Keep `adoptionGate=false` and the existing default authorizer.

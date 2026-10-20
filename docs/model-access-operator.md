# Native model access projection

[简体中文](model-access-operator.zh-CN.md) | English

This opt-in stage of [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338)
adds an actual controller to the existing Kubebuilder Operator. It consumes
admitted [ModelAccessGrant](model-access-grants.md) session bindings and writes
owned Authorino v0.26.4 `AuthConfig` and freshness ConfigMap resources. It does
not replace the default authorizer, connect shared Higress ingress or establish
complete product acceptance. Workloads require the [opt-in native verifier](workload-native-access.md).
Adoption remains gated.

## Ownership and configuration

DB authorization remains authoritative. Use the existing transaction outbox and
publication relay; this controller has no DB or Secret access of its own and
adds no alternate publisher. Grants must carry the exact binding name, relay
markers, deployment label and current DeploymentConfig identity. Human and
agent **session** bindings are supported; Kubernetes workload bindings fail
closed by default. The separate [native workload verifier stage](workload-native-access.md)
requires an explicit audience and TokenReview RBAC; unconfigured deployments keep the original behavior.

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

### Employee identity storage prerequisite

Migration 055 adds an internal, credential-free registry for the explicit AEP
agent ID, employee namespace/name/UID, dedicated ServiceAccount name/UID and
gateway audience. It is **not** a public registration API or an authorization
grant. A future trusted lifecycle writer must read the current employee and
its controller-owned SA, verify both UIDs and the explicit agent mapping, then
call the repository. Neither a caller header nor the human owner is evidence.

Registration uses an expected-current binding key. Replacing an SA retires the
old binding atomically; stale observations cannot overwrite its replacement.
Historical SA identities cannot be reassigned or resurrected. Account deletion
retires live observations while preserving history. Each agent has at most one
current binding. Multiple replicas
of that employee use its dedicated SA; this is not a per-conversation registry.
Trusted observation freshness is bounded to 2 seconds–1 hour, using the database
clock. This is an admission range, not a production propagation recommendation.
Expired/retired observations are absent from the live business source.

Binding changes, retirement, shorter verification bounds and recovery after
expiry queue the existing targeted source signal in the same transaction.
Continuous freshness extension does not invalidate authorization. Role-derived
models and memberships remain unchanged. The source reports the observation's
expiry as another natural deadline.

This prerequisite does not start a Kubernetes watcher, issue/read tokens,
publish workload grants, configure TokenReview, or make the runtime consume a
token file. The session-only producer refuses live workload registrations and
leaves their source signals unacknowledged; the native Operator still refuses
workload grants. Do not enable employee model access based on registry storage.
The trusted writer, combined binding producer and data-plane integration remain
separate stages under [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338)
and [governance #131](https://github.com/rongxinzy/aep-deerflow-governance/issues/131).

The existing administrative M2M token endpoint issues a model JWT for a target
user; it does not prove an employee's Kubernetes identity. This registry does
not change that endpoint or turn its caller-supplied conversation ID into a
trusted workload binding.

### Session projection

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

### Optional real shared Higress ingress acceptance

On top of business mode and HTTPS settings, set `AEP_NATIVE_ACCESS_HIGRESS=true`
and `HELM`. `AEP_HIGRESS_CORE_CHART` may point to a local Higress core **2.2.4**
chart; otherwise that fixed version is fetched from the official repository.
Docker must cache matching `gateway`, `higress` and `pilot` images (full addresses
in `tests/e2e/auth-cache/higress-install.mjs`). This mode installs real Higress
CRDs/controllers instead of the schema fixture.

Native Envoy gRPC `ext_authz` and per-binding route `context_extensions.host`
map the untrusted, test-only `x-aep-access-binding` hint to an AuthConfig.
Authorino still validates the actual JWT subject/session and request body model;
the hint grants no permission and is not a published public contract. A
credential-free echo upstream reports sensitive-header presence only, never
tokens or body contents. The inference Host/path remain unchanged; bearer,
binding and tested identity/Consumer/internal-observation headers are removed
before upstream forwarding.

Checks cover cross-session/missing/unknown hints, invalid bearer, forged
identity, actual model escalation, malformed and oversized bodies. An independent
upstream counter confirms denied requests never reach the upstream. Business
role/team/model changes, session revocation and Control/relay outage expiry and
recovery also pass through real Higress, alongside the existing per-Pod checks.
Stopping every authorization process must fail closed, then recover on restart.

An initial HTTP `ext_authz` formatter attempt was rejected by the real gateway
with `:-prefixed or host headers may not be modified`. Formatter support does
not establish support for that use. The gRPC fixture explicitly creates routes
for actual sessions; production ownership, dynamic route publication/retirement,
all-process adoption and capacity costs remain unimplemented/unverified.
An explicit limitation check demonstrates that unlisted `x-aep-*` headers,
including dynamic rate-limit headers, still reach the echo. This is recorded as
a known limitation, not a security capability pass. Adoption requires sanitizing
the entire reserved header namespace before deriving trusted quota identity;
the fixture must not be adopted directly.

This is isolated acceptance configuration, not a production default ingress.
Provider inference, enterprise entitlements, employee workloads, trusted
Consumer/Redis quota identity, NetworkPolicy and cache-hit evidence remain
unverified; no trusted identity-header delivery is implemented here. These
product adoption gates remain open and `adoptionGate=false`.

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

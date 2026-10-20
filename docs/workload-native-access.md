# Native employee workload authentication

[简体中文](workload-native-access.zh-CN.md) | English

This opt-in gateway stage of [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338)
and [employee access](https://github.com/rongxinzy/aep-deerflow-governance/issues/131)
lets the existing Kubebuilder Operator project Kubernetes-bound ModelAccessGrant
resources to native Authorino v0.26.4 TokenReview and OPA configuration. Defaults
and existing inference paths remain unchanged.

## Trust and deployment

DB role assignments remain authoritative. The trusted publisher must verify the
employee CR UID, dedicated SA name/UID and explicit AEP agent identity before
publishing through the existing outbox/relay. An employee is not its conversation
owner. The optional lifecycle observer below records that evidence; it does not
publish grants. Mixed session/workload publication and licensed employee
inference remain separate, incomplete stages. DeerFlow's optional token-file
consumer is tracked in [deer-flow#34](https://github.com/rongxinzy/deer-flow/pull/34).

### Optional trusted lifecycle observer

`services/control-service/cmd/workload-observer` is a separate, opt-in process,
not an HTTP registration API or a component of the default images/overlays. It
requires the credential-free `status.modelIdentity` contract from
[governance#149](https://github.com/rongxinzy/aep-deerflow-governance/pull/149) and
the existing workload registry migration, applied by the normal control-service
migration path. It never migrates the database itself.

The native controller-runtime Manager watches DigitalEmployees and their
ServiceAccounts in one configured namespace. Events only schedule work. Live
APIReader reads verify the explicit `spec.modelAccess.agentId`,
`spec.governance.deploymentID`, CR UID/current observedGeneration, audience and
the dedicated SA's exact name/UID, controller owner and disabled automatic token
mount. The observer rereads the employee to fence changes during the SA read.
It never substitutes `defaultAEPUser` or a conversation owner for the agent.

The existing DB registry checks agent account kind, immutable identity history
and compare-and-swap replacement. Healthy evidence renews at half the configured
freshness; a stale status or transient API error pauses renewal without retiring
the identity. Positive live evidence can renew an expired current observation.
Confirmed employee/SA deletion, explicit binding removal or a foreign deployment
retires exact previously observed keys. Deleted objects missed while the process
was offline remain bounded by observation expiry; this is not an atomic
Kubernetes/DB transaction or an immediate Pod-deletion revocation mechanism.

Render the optional bundle before adapting and installing it:

```bash
kubectl kustomize deploy/kubernetes/workload-observer
```

Configure these explicit inputs in the deployment overlay:

| Setting | Requirement |
| --- | --- |
| `AEP_ENVIRONMENT` | `production`, `development` or `test`; no implicit environment |
| `AEP_WORKLOAD_DATABASE_URL_FILE` | Mounted DSN from `aep-workload-observer-config`, key `database-url`; mutually exclusive with `AEP_WORKLOAD_DATABASE_URL` |
| `AEP_WORKLOAD_DEPLOYMENT_ID` | The exact AEP deployment and employee governance binding |
| `AEP_WORKLOAD_NAMESPACE` | One namespace, matching the scoped Role/RoleBinding |
| `AEP_WORKLOAD_AUDIENCE` | The exact projected-token/grant audience |
| `AEP_WORKLOAD_FRESHNESS` | Explicit whole seconds, from `2s` to `1h`; sample `1m` |

Production DSNs require verified TLS with no insecure fallback. Provision a
dedicated DB identity for the registry and its existing source-event triggers;
the sample does not grant DB privileges. Namespace/deployment/audience values
are delivery inputs, not auto-discovery. Install the two prerequisite namespaces
and governance CRD first; resolve the `ko://` image using the repository's ko
workflow, pin the resulting digest, and narrow API/DB egress for the target CNI.
No provider key, keyring, credential Secret read/write or TokenReview permission
is granted to the observer. Only the fixed `aep-workload-observer` Lease is
read/updated, with native leader cancellation and bounded reconcile contexts.

Port 8096 `/healthz` is dependency-independent process liveness; `/readyz` checks
DB reachability and initial informer synchronization with a bounded context.
Neither proves registry freshness, publication, model permission or serving-replica adoption.
Stopping the observer pauses renewals; retained history must not be deleted to
force a rollback. This stage adds no schema, public API, SDK or production
cutover, and leaves `adoptionGate=false`.

Configure the existing [projection](model-access-operator.md), plus:

- `AEP_GATEWAY_ACCESS_WORKLOAD_AUDIENCE`: explicit trusted audience matching both
  grant and projected token; at most 256 bytes, with no whitespace/control characters.
- `AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT`: defaults to true. Enabled workload
  grants need an explicit, exact [published workload entitlement](workload-entitlement.md).
  SA identity alone is not enterprise entitlement. Disabled tombstones can
  always remove access, including after their license evidence is removed.
- `deploy/kubernetes/access-workload-review`: separately applied RBAC, absent from
  default overlays. It grants only `create` on cluster-scoped TokenReviews to
  `aep-system/authorino`. It does not install Authorino/create its SA or grant
  Secret, TokenReview or SubjectAccessReview access to employees/the Operator.
  Deployment identities that differ require an explicit binding adjustment.

The verifier must trust the API Server issuing the employee token. This stage
does not provide cross-cluster identity federation.

## Request and cache bounds

Native TokenReview authenticates the exact bearer; OPA matches the complete SA
username, UID and audience. Identity and complete access results use the exact
bearer as their native cache key. New replicas and rotated tokens are cold.
There is no request-time business DB lookup, custom verifier or custom cache.

A separate uncached native OPA check parses the **same previously reviewed** JWT
using `io.jwt.decode`, requiring numeric exp/nbf/iat, exact sub/aud/namespace/SA
name and UID, a nonempty bound Pod UID, actual JSON body model membership, semantic
hash and absolute publication deadlines. Decoding alone does not authenticate a
signature and must never replace TokenReview. Review results lack exp, so the
uncached JWT time check is mandatory even when identity authentication is cached.

Role/model/binding changes update the affected grant; freshness-only renewal
preserves AuthConfig and its caches. Existing role/team/Consumer quota identity
is reused, never a Pod name or owner identity.

Cached TokenReview does not watch Pod/SA deletion. A trusted lifecycle publisher
must publish disabled tombstones on deletion/replacement; publication deadlines
bound stale state. This stage does not promise immediate Pod-deletion revocation
or zero propagation delay. Recreated SA names cannot satisfy an old SA UID. The
employee CR UID is a trusted publication association, not a claim in an SA JWT.
No mTLS, opaque-token or non-Pod-bound token support is supplied.

## Isolated verification

Use the prerequisites and pinned images of the [native projection probe](model-access-operator.md):

```bash
AEP_NATIVE_ACCESS_WORKLOAD=true \
AEP_NATIVE_ACCESS_WORKLOAD_EXPIRY=true \
AEP_AUTHORINO_CRD_PATH=/path/to/pinned/authorino.kuadrant.io_authconfigs.yaml \
node tests/e2e/native-access-projection.mjs
```

The fixture creates/cleans its own Kind cluster and two independent Authorino
processes. It tests actual Pod-bound tokens, exact identity isolation, model scope,
publication renewal/expiry, rotation and disabled grants. API Server counters
prove repeated warm calls do not issue TokenReview. Expiry mode waits for the
real ten-minute Kubernetes token expiry without changing clocks or signatures,
then verifies warm denial without another TokenReview.

This is verifier/Operator acceptance, not employee product acceptance. Lifecycle
writing, business role-to-grant publication, DeerFlow calls, verified-license DB
publication, Higress provider inference and Redis quota debit are not covered.
Add `AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT=true` to test the native workload
license grace, exact selection, replacement and removal boundaries.
Ready does not fabricate all-replica adoption; `adoptionGate` remains false.

## Live Pod fencing (stage one)

The employee business probe in [#370](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/370)
confirmed a deleted Pod's unexpired bearer remained accepted by warmed verifiers
after the API Server rejected a fresh TokenReview. Token expiry and a stable SA
alone do not prove that the original Pod remains live.

`ModelAccessGrant.spec.workloadPodUids` and the same canonical-snapshot field
carry at most 512 live Pod UIDs observed by a trusted publisher. This mutable
evidence sits outside the immutable binding: changes retain employee identity,
route binding and quota Consumer. Session publications cannot carry it. Canonical
snapshots require sorted, unique, bounded UIDs; relay preserves the exact set and
continues to fence same-version mutations and old-publication replays.

The native uncached OPA bounds require the authenticated bearer's Pod UID to be
in the current set. Changes update only the freshness-bounded external policy,
without rewriting AuthConfig or evicting identity/decision caches. A new Pod's new
bearer still needs its first TokenReview. Missing/empty evidence denies workload
requests; there is no fallback to merely checking a nonempty Pod UID.

This stage supplies the contract, relay and native guard, not database evidence,
trusted Pod watches or complete deletion/replacement business acceptance. It does
not close #370 or enable adoption. Install the CRD first, then the verifier and
evidence-producing components. Old publications without the field will deny;
keep experimental traffic isolated. Publication deadlines, bounded propagation
and `adoptionGate=false` remain required. Test-written isolated Grants do not
prove production lifecycle publication is connected.

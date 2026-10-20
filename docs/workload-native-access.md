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
owner. This stage consumes trusted grants; it supplies neither that lifecycle
writer nor workload publication nor DeerFlow token-file consumption.

Configure the existing [projection](model-access-operator.md), plus:

- `AEP_GATEWAY_ACCESS_WORKLOAD_AUDIENCE`: explicit trusted audience matching both
  grant and projected token; at most 256 bytes, with no whitespace/control characters.
- `AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT`: defaults to true. Workload grants are
  rejected in that mode because SA identity is not an enterprise entitlement.
  This opt-in probe is not a licensed workload implementation.
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
writing, business role-to-grant publication, DeerFlow calls, enterprise
entitlements, Higress provider inference and Redis quota debit are not covered.
Ready does not fabricate all-replica adoption; `adoptionGate` remains false.

# Published employee workload entitlement

[简体中文](workload-entitlement.zh-CN.md) | English

This is an opt-in Kubernetes publication contract for the existing Kubebuilder
access Operator. It does not add an HTTP API, replace human activation or turn a
Kubernetes identity token into an AEP entitlement JWT.

## Evidence and trust

An enabled workload grant in a deployment requiring entitlement must include
`spec.workloadEntitlement`: `licenseId`, `licenseDigest` and the exact feature
`enterprise.models`. The ID and digest must match one entry of `spec.licenses`.
Only a Kubernetes binding can contain this attestation. Admission rejects an
unknown license, mismatched digest, another feature or same-version mutation.

The existing trusted DB producer must derive the attestation in the same locked
snapshot as the account's effective role grants and trusted workload observation.
It must select a registered, signature-verified, deployment-matching, active
license whose features explicitly contain `enterprise.models`. It must not infer
that feature from an arbitrary active license. Never use the employee owner,
conversation initiator or request headers to pick identity or license.

The attestation is not a signed license, bearer credential, activation record or
additional end-user assignment. Its integrity comes from exclusive-writer RBAC
and the existing immutable publication/outbox/relay guards, just as the derived
model allow-set does. CR/status write access is privileged. No provider key or
license private key belongs in these resources or the employee Pod.

## Enforcement and lifecycle

Native TokenReview authenticates the exact Pod-bound ServiceAccount bearer.
Native OPA separately enforces the trusted grant's identity/model bounds and
the selected license's absolute grace deadline, even with a warm identity and
authorization result. A nil license deadline means perpetual. Another active
license in the accepted list cannot substitute for the selected ID/digest.

License replacement requires a newer publication with an explicit new selection.
The selection participates in this binding's semantic hash; freshness-only
publication renewal does not rewrite AuthConfig. On license removal, revocation,
feature removal or expiry, the producer must publish a disabled grant with an
empty model set and remove its attestation. Such deny tombstones remain renderable
with `RequireEntitlement=true` even without an accepted license. Admission and
the renderer reject a malformed attestation rather than silently ignoring it.

Deployment entitlement remains required by default. Workload audience remains
empty by default; no default traffic switches. Without an attestation, an enabled
workload still fails closed when entitlement is required. Human requests continue
using signed AEP entitlement JWTs with their exact license ID/digest and session.

Natural license expiry is an absolute request-time bound. Revocation propagation
still depends on publication and is bounded by the grant's freshness; this is not
zero-delay revocation. Kubernetes TokenReview cache limitations still apply.

## Verification and release order

The immutable canonical snapshot includes the attestation, so changing or
stripping it changes the content hash without changing the workload binding key.
The existing relay transports it unchanged and rejects same-version corruption
or a pruned read-back.

The opt-in mixed business producer now selects the license and creates this
attestation from the same locked source as the role grants and live workload
observation. It retains its previous exact ID/digest while that license remains
eligible; otherwise it selects the lexicographically first eligible license ID.
Only the selected license enters a workload publication. Adding or changing an
unrelated license does not change its semantic hash or existing operation.
The human session producer retains its existing accepted-license contract.

Missing, revoked, expired or feature-ineligible licenses produce disabled,
nonretired workload policies without an attestation. Later valid evidence can
recover the same identity. Confirmed identity retirement also clears the
attestation. The selected grace deadline caps publication and durable recapture;
natural expiry does not require a DB write. Migration 056 makes `revoked_at`-only
changes signal recapture and seeds affected deployments on upgrade. It does not
rewrite applied migrations or remove immutable binding history.

Run the [native workload probe](workload-native-access.md) with
`AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT=true`. It uses explicit CR fixtures and two
real native verifier processes to test grace expiry on warm results, no fallback
to another active license, exact replacement, tombstones and unaffected peers.
It does not prove the integrated business path. PostgreSQL producer regressions
separately cover atomic publication, recoverable license withdrawal/restoration,
deterministic replacement, unrelated-license stability and real grace expiry.
Repository tests use synthetic persisted license metadata. An additional
PostgreSQL application regression creates a disposable in-memory signed license,
runs the production verifier and `RegisterLicense`, then captures the exact
license ID/digest and role-derived employee model policy without a human session.
A valid signature for another feature still denies model access. This is not an
HTTP/browser import test. Runtime delivery, Higress inference, Redis quota debit
and all-serving-replica adoption still require integrated acceptance.

Upgrade the generated CRD, access Operator and snapshot/relay readers before enabling a producer that
publishes this field. An old Operator with entitlement required denies enabled
workloads. An old CRD can prune unknown fields, so delivery must require exact
read-back rather than treating an API write as success. Keep `adoptionGate=false`
until integrated business and failure acceptance establishes the full boundary.

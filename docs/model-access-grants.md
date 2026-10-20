# Effective model-access projection

[简体中文](model-access-grants.zh-CN.md) | English

This is the Kubernetes contract stage of [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338),
following [the component evidence](auth-result-cache-feasibility.md). It adds
`gateway.aep.zhiyuan.io/v1alpha1 ModelAccessGrant` to the existing kubebuilder
project. It does **not** enable a controller, publish business authorization,
install Authorino or change the default gateway path. Public HTTP/SDK APIs are
unchanged; new public APIs, if needed, must follow OpenAPI-first development.

## Source and scope

PostgreSQL remains the business source. Permissions, models and plugins are
assigned to roles; human and agent accounts acquire them through effective
roles, including team defaults. This CR is a derived effective allow-set, not
a new assignment API or a copy of the role graph. Existing `ModelPolicy` is
still exclusively rate/quota intent and does not grant access.

One grant covers one deployment/account/session or trusted workload binding.
`subject.id` is the AEP `users.id`, including `kind=agent` service accounts,
never the employee owner's identity. A role/team change republishes only
affected grants; an unrelated grant and its valid cached result stay intact.
The publisher must use a deterministic hashed name for the complete binding.
Names, URLs and caller-provided headers are selectors, never proof of identity.

- Human grants bind the verified model JWT's `sub`, `deployment_id` and
  `session_id`; issuer/audience/use/time/scope and current session state remain
  mandatory. The client never supplies its effective allow-set.
- Employee workload grants bind the agent account to a dedicated Kubernetes
  ServiceAccount **UID**, namespace, audience and DigitalEmployee UID. A
  recreated ServiceAccount or employee cannot inherit the old binding. The
  native verifier must establish workload identity; a CR declaration alone
  does not authenticate a Pod. The lifecycle mapping and verifier integration
  are separate implementation/acceptance stages. No provider key, application
  model token, password, refresh token or signing key belongs in the CR.
- `models` holds AEP model IDs, not provider-native names. Empty denies all.
  `roleIds`/`teamIds` are trusted derived membership for existing native quota
  and observation identities, not independent user/team resource assignments.
- `licenses` preserves accepted license ID/digest pairs and their grace deadlines
  for enterprise entitlement tokens. The native verifier must match **both**
  `license_id` and `license_digest`, as well as deployment/subject/session, and
  enforce the deadline per request even on a warm result. A perpetual license
  omits `validUntil`. Empty/omitted bindings deny entitlement tokens; they never
  waive the deployment's requirement for an entitlement. Model/workload token
  acceptance still follows the reviewed deployment verifier configuration. No
  signed license body, activation secret or private material belongs here.

## Publication and invalidation

`publication` reuses the existing sequence/operation/revision/content-hash
correlation. The producer must hash a canonical payload excluding the hash
field itself and keep each snapshot immutable. The existing transaction
outbox and publication relay remain the only intended DB→CR path. This stage
does not implement that producer or grant a new writer Kubernetes permissions.

Admission makes deployment, subject and binding immutable. A changed spec
requires a strictly greater publication sequence; a repeated identical spec
is permitted. Same-version edits and sequence rollback fail even if the
writer has update permission. Deletions/recreations need additional lifecycle
fences: CEL cannot remember a deleted object's history. Retain disabled,
empty-model tombstones until the durable outbox/history retention fence proves
old deliveries cannot replay; restrict deletion and ownership in RBAC.

`issuedAt` and `validUntil` form a positive publication validity window capped
at one hour. This is an admission ceiling, not a default outage tolerance or
token lifetime. The publisher must choose a shorter reviewed freshness bound
and must not extend token/session/license validity. Expired snapshots must
deny on the request path even with a cached allow decision. Admission validates
shape/order, not wall-clock freshness or a token's signature.

Renewal must be an explicit, monotonically versioned publication from current
business state. It cannot blindly prolong the last snapshot when the DB,
license checks or delivery chain are unavailable. The integration must prove
that renewal/freshness handling preserves valid-result reuse; arbitrary
periodic cache flushing is not an acceptable substitute. This contract alone
does not establish that a selected component meets that requirement.

User roles, team memberships/default roles, role model assignments, model or
account enablement, password-change restrictions, session withdrawal, employee
deletion/disablement and license changes must all have transactional signals.
Expiry/failure handling and impacted-principal expansion remain implementation
work. A direct fixture edit is not evidence for this business pipeline.

## Observation and safety gates

`status` is a separate subresource. `Accepted` may describe semantic validation;
`Applied` may describe owned resource writes. `Ready` additionally requires
independent observations from **all currently serving processes** at the
current publication, with fresh timestamps. Each observation records Pod UID
and process incarnation to distinguish container restarts within the same Pod.
A CR generation, Ready Pod, Lease leader or successful apply is insufficient.
The observer must discover the current serving fleet independently; a supplied
partial list cannot establish completeness. No such observer is implemented
by this contract PR.

The serving path must deny missing/deleted/disabled/expired grants, reject
cross-binding/model reuse, sanitize identity headers, preserve hashed native
Consumer/Redis keys and continue rate/quota accounting per call/usage. Accepted
streams are not automatically terminated by CR updates. Replica-local cold
evaluation after first use, restart or eviction is allowed; valid warm results
and unrelated principal results must still be reused.

Use `bash services/gateway-operator/generate.sh` to generate deepcopy and CRD
artifacts. Real envtest admission covers version/identity/license fences, deny
tombstones, validity and status isolation. Product adoption still requires
business API→DB→outbox→relay→CR→Operator→all serving replicas, replay/failure/
recovery tests, trusted human/employee binding, capacity/concurrency and native
quota compatibility. Keep the existing authorizer until those gates pass.

# Effective-access publication bytes

This internal package shares the immutable access payload between the business
producer and the existing publication relay. It is not a public HTTP/SDK API,
an identity verifier, a role-policy interpreter or an authorization-result cache.
`ModelAccessGrant` remains the Kubernetes execution contract.

Canonical policy bytes contain the full immutable deployment/subject/binding,
role-derived model allow-set, trusted quota memberships and exact accepted
license ID/digest/deadline pairs. They exclude source version, operation ID,
sequence, observation time and publication freshness. The ledger stores these
separately. A relay must validate the canonical bytes and binding key, then add
the exact ledger publication reference and issued/valid timestamps to the CR.
It must never reconstruct intent from the mutable catalog or treat a CR write
as adoption by all serving processes.

`CaptureModelAccessSessions` uses the shared configuration transaction lock
before the source/signal/stream locks. It captures the union of current and
previous session bindings, queues every immutable snapshot/outbox entry, and
acknowledges the exact source version in the same transaction. Any failure rolls
the whole operation back. Same effective policy and safe unchanged validity reuse
the prior operation; changed deadlines are real delivery work. Source-provided
ordering is normalized independently of database collation.

A removed binding has `retired=true`, `disabled=true` and an empty model set.
Retirement is a permanent ledger lifecycle fence. Reusing a deleted session ID
cannot reactivate an old signed binding. Tombstones and stream history must not
be garbage-collected without an approved replay/retention fence. A user with no
role models has an empty allow-set, which is distinct from retiring the session.

Natural time passage does not emit a business-write event. Migration 054 adds
durable `next_capture_at` work to the existing targeted-signal table. Due work
advances its source version transactionally. Freshness refresh is scheduled
before the explicit cap; a shorter model/session/license deadline is re-evaluated
at expiry, never blindly extended. Bounds use whole seconds to match
`metav1.Time`; an imminent subsecond security deadline is retried without
acknowledgement or rounding up. Freshness input must be whole seconds, 2s–1h;
this admission range is not a recommended production propagation window.

Bounded discovery is cursor-based and does not claim work. The existing relay
must process subjects independently and back off per subject: a legacy direct
grant compatibility error must not block unrelated subjects. Active legacy
user/team model assignments require migration before adoption. Account
disablement still permits deny capture even when such assignments exist.

This stage implements session production only, including agent-account sessions.
It refuses to acknowledge a subject with existing Kubernetes workload bindings
until the trusted employee lifecycle mapping stage can capture all bindings.
It does not start another worker, write Kubernetes, alter default authorization
or assert that a native freshness renewal preserves warm cached decisions. That
renewal property, trusted workload binding, existing relay/Operator integration,
all-serving-process observations and product acceptance remain separate gates.

Integration prerequisites are AEP PRs #344, #345, #346 and #348; PR #347 is the
continuous real-PostgreSQL validation prerequisite. Migration 054 requires 052.
When these prerequisites are not yet in main, this branch carries their exact
files unchanged; only this stage's new files are modified. Do not merge this
integration stage ahead of its prerequisites. Local verification also uses an
explicit disposable server, separately from remote CI and product acceptance.

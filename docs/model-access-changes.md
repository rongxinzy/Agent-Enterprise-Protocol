# Transactional model-access change signals

This is the database-input stage of [effective access publication #338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338).
Migration `052_model_access_changes.sql` adds a durable, coalescing outbox input
for the **existing publication relay**. It does not enable a relay consumer,
create a Kubernetes writer, replace the current authorizer, or prove business
DB → CR → data-plane delivery. The independent `ModelAccessGrant` contract is
proposed in [#344](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/344).

## Storage and targeting

`model_access_changes` has one row per `(deployment_id, user_id)`, including
digital employees' own `users.id`. It contains only an increasing signal
version, an acknowledged version and an update time, with no credentials,
tokens, policy payload or copied identity metadata. Existing users are seeded
once during migration. Versions are work fences, not event counts or CR
publication sequences. Multiple changes can coalesce into one pending row.

Database triggers record changes in the **same transaction** as the business
write, including callers using pgx rather than GORM. They expand direct roles,
team membership and team default roles. Dependency expansion deliberately
includes disabled memberships to capture former authorizations; the existing
`user_effective_roles` remains the enabled-role authorization relation. Only
the affected deployment is expanded for license changes.

| Business input | Signal recipients |
| --- | --- |
| Account status/kind, password/security state, creation/deletion | That user |
| Direct role or team membership | Old and new user |
| Role enabled/deleted or permission binding changed | Direct and inherited role members |
| Team enabled/deleted or default roles changed | That team's members |
| Model assignment added/removed/moved/expiry edited | Old and new assignment dependents |
| Model disabled/enabled/deleted | Dependents of assignments naming that model |
| Session creation/revocation/deletion | That session's user |
| Employee profile creation/deletion/expiry edited | That employee, never its owner |
| License/activation creation, removal, revocation or relevant signed-state edit | Users in that deployment |

Parent deletions are captured before cascading memberships/assignments vanish.
User deletion retains its signal row so a later consumer can publish a deny
tombstone. Reusing an ID does not reset its version. A deployment cascade does
not recreate orphan signal rows. Existing business foreign-key restrictions
remain in force.

Fanout and membership/grant writes share the existing deployment-scoped gateway
configuration transaction lock (`hashtextextended(deployment_id,101)`). Without
this ordering, a new member could be processed against old policy while a
concurrent policy change misses its uncommitted membership. The fanout query
takes a fresh snapshot after lock acquisition. Tests exercise both commit
orders. This serializes configuration writes within a deployment, not model
requests; normal PostgreSQL deadlock/serialization errors still require caller
retry, and do not commit partial business changes or signals.

Names, descriptions, email, UI labels, primary-membership flags, session or
activation heartbeats, and refresh-token rotation produce no policy signals.
An identical UPDATE also produces none. A repository's delete/reinsert of an
equivalent binding can still produce signals: a consumer **must compare
canonical effective output before creating a new policy publication**. Reading
a signal must never itself flush a warm result or another principal's cache.

Historical user/team assignment dependencies are captured to support safe
cutover and revocation, not to introduce direct resource grants. The target
design continues to assign resources only to roles. This migration does not
change existing assignment APIs or entitlement evaluation.

## Delivery boundary and remaining work

Pending rows satisfy `version > acknowledged_version`. A future relay consumer
must capture the effective snapshot and advance the acknowledged version in one
transaction, with a row lock or expected-version fence; it must not acknowledge
before durable snapshot/outbox capture. A stale acknowledgement must not consume
a newer change. Only trusted publication code should write acknowledgements.
Kubernetes delivery/adoption needs its own immutable publication record and
per-binding sequence; this table is not evidence of CR application or gateway
adoption. It must not be polled on every model request.

There is **no wall-clock expiry worker, lease renewal or freshness guarantee in
this stage**. Editing an assignment/profile/license expiry generates a signal;
merely reaching that time does not fire a database trigger. Subsequent snapshot
and runtime stages must bound validity by source expiries, schedule due work,
handle session-token liveness/renewal without periodically flushing unchanged
policies, and fail closed on publication/watch failures. The default safety
path stays enabled until those integration gates pass.

## Verification

Use a disposable PostgreSQL server with permission to create databases:

```sh
export AEP_REFERENCE_TEST_DATABASE_URL='postgres://…'
go test ./services/control-service/internal/repository -run TestModelAccess -race -count=1 -v
```

Each test owns a randomly named database, runs all real migrations, and drops
only that database. The tests cover dependency targeting, inherited agent
roles, unrelated tenants/users, binding moves, ignored writes, rollback,
delete/cascade ordering, ID reuse, concurrent version increments and stale
acknowledgements, both membership/policy commit orders, and upgrades from the
previous schema with existing users. Without the explicit environment variable, these PostgreSQL
tests skip; mocks cannot replace this evidence. No real model or customer
credential is required.

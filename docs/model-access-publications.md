# Per-binding model-access publication ledger

This is the durable delivery stage of [effective access #338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338).
It adds storage/repository operations for the **existing publication relay**,
without starting a consumer, enabling another Kubernetes writer, changing the
default authorizer, or exposing an HTTP/SDK endpoint. Business authorization
derivation and data-plane adoption remain separate integration stages.

## Streams and immutable input

Migration `053_model_access_publications.sql` adds three tables:

- `model_access_streams`: a retained subject/binding identity and its highest
  source-work version and publication sequence.
- `model_access_publications`: immutable JSON bytes, digest, revision, validity
  bounds and operation identity; delivery phase/error are mutable.
- `model_access_publication_outbox`: leased claims, retry delay, attempt count
  and completion. Claim tokens fence acknowledgement after worker crashes.

One stream covers one immutable subject/session or trusted workload binding.
The internal producer must derive `binding_key` from the full binding identity;
the ledger rejects reusing it for another subject. Each binding has its own
sequence. A newer Alice intent supersedes only Alice's undelivered operations,
never Bob's, even when both are in one deployment. A newer queued intent also
invalidates old projected evidence; an older successful operation is no fallback.

`QueueModelAccessPublication` accepts an **already-derived, credential-free**
`ModelAccessIntent`. The producer owns role authorization, trusted identity and
projection schema validation. This is internal persistence, not a generic API
for client-supplied JSON. No signing key, cloud key, token or resolved Credential
may enter the payload. The ledger checks object JSON, a one-MiB limit, its exact
SHA-256, identity bounds and a positive validity interval of at most one hour.
Expired or future-issued inputs are rejected against database time.

Payloads use `bytea` so PostgreSQL JSON normalization cannot change their exact
bytes or digest. A database trigger rejects mutation of snapshot inputs. Stream
identity/version rollback and deletion are rejected while the deployment exists;
user/session/CR deletion must not reset the fence. Deployment removal cascades
its artifacts. No automatic garbage collection is enabled.

The producer supplies a durable monotonic `source_version`, not a timestamp or
untrusted request field. Older input is rejected; the same version cannot change
content or validity. A higher source version with identical payload hash and
valid-until keeps the same operation/sequence, even if a cosmetic revision label
changes. The stream still advances its source fence so older work cannot replay.
Canonicalization of equivalent model/role sets belongs to the producer: merely
changing JSON formatting is not equivalent immutable input at this layer.

Changing validity is new delivery work. The ledger does **not** establish safe
renewal or prove that a native authorization cache survives renewal. A terminal
failure is stable for the same input version; new source work may retry the same
policy under a new publication sequence.

## Transaction and worker integration

Called through a `DeploymentStore` provided by `WithTransaction`, queue capture
joins that transaction. A later producer must read the effective business state,
persist snapshots/outbox and acknowledge its source signal atomically, with the
existing configuration lock and expected-version fence. No network or Kubernetes
operation may run inside this transaction. The source consumer is not wired in
this stage; [#345](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/345)
provides the independently mergeable change-input stage.

`ClaimModelAccessPublication` claims one current stream with `FOR UPDATE SKIP
LOCKED`. Other bindings progress while one is busy. Live claims last 30 seconds;
an expired claim is recoverable under a new token without changing immutable
input. Expired publication input becomes an explicit
`ACCESS_SNAPSHOT_EXPIRED` failure instead of being redelivered.

`FinishModelAccessPublication` accepts only the latest binding operation's
unexpired claim and unexpired snapshot. Empty phase retries after a bounded
delay; `projected` means CR delivery, **not all-serving-process adoption**.
Terminal failures carry bounded structured codes, not raw SQL/provider errors.
Native CR ownership and resourceVersion/sequence checks must separately fence
late Kubernetes writes. The future adapter must validate schema/hash again.

## Dependencies and remaining gates

The migration has no dependency on the signal tables in migration 052, or the
`ModelAccessGrant` type proposed by [#344](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/344).
There are no overlapping files. Subsequent integration needs those stages and
must extend the existing relay, not create a parallel writer. The ledger alone
does not produce business grants, revoke runtime access, renew safe leases,
implement all-replica confirmation, migrate legacy direct user/team assignments,
or complete human/employee model-request acceptance. Keep the current safety
path until those gates pass.

## Verification

Explicitly set `AEP_REFERENCE_TEST_DATABASE_URL` to a disposable PostgreSQL
server that permits creating databases, then run:

```sh
go test -race ./services/control-service/internal/repository ./services/control-service/internal/db
```

Each integration test owns and drops a randomly named database. Tests cover
atomic rollback, byte/hash preservation, immutable inputs, per-binding sequence
and unchanged output, stale source replay, cross-deployment and subject binding,
supersession, lease expiry/crash recovery/backoff/terminal repair, concurrent
idempotency, unrelated busy-stream progress, publication expiry and invalid
inputs. Without the explicit environment variable, PostgreSQL tests skip.

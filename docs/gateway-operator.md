# Gateway operator development

The gateway operator uses kubebuilder API markers, controller-gen and the
controller-runtime Manager. This is a staged migration from the DB-driven
gateway reconciler; no temporary shadow runtime is required.

## Gateway configuration contract

- One `DeploymentConfig` named `deployment` per operator namespace owns the
  aggregate ai-proxy, ai-statistics, native limit and quota-admin resources.
  Its immutable `deploymentId` preserves existing resource names. The current
  spec covers gateway settings only, not compute or business defaults.
- `ModelRoute.deploymentRef` defaults to `deployment`; `providerType` preserves
  native provider adapters such as `deepseek`. Protocol `openai` projects to
  the existing `openai-compatible` wire name. Route controllers own only their
  Ingress/EnvoyFilter resources.
- `ModelPolicy` selects one route. An omitted subject means model scope;
  user/team/role subjects intersect with the selected model using existing
  trusted AEP authorizer headers. The subject is an identifier, never a
  client-supplied authentication header.
- Rate limits count requests. Interval quota defaults to `requests`; explicit
  `unit: tokens` counts Tokens. This is an interval limit, distinct from the
  existing ai-quota balance-management API. Intervals are exactly 1, 60, 3600
  or 86400 seconds, matching native Higress plugin units.
- CRs contain Secret name/key references only. Namespace selection belongs to
  operator deployment configuration. No credential value is accepted in a CR.
- `Accepted` is semantic acceptance, `Applied` means Kubernetes writes are
  complete, and `Ready` requires independent data-plane evidence. SSA alone
  must never produce `Ready=True`.
- Applied-resource inventories are observations, not authority to delete
  arbitrary objects. Controllers must verify live UID ownership before cleanup.

## Development and migration boundary

Run `bash services/gateway-operator/generate.sh` for CRDs, deepcopy and RBAC.
Use `KUBEBUILDER_ASSETS` with the pinned envtest assets for real admission,
watch and status tests. Existing Kind/Higress acceptance remains required
before production cutover; envtest does not run Envoy or native plugins.

The legacy synchronous `/aep/v1` API remains unchanged. The additive async
API/SDK, transactional outbox and opt-in relay are documented in the
[publication contract](api-v1.md#asynchronous-operator-publication-queue-stage).
Bounded consistency, DB reference validation and full cutover acceptance remain
required before routing Console writes to CRs.
Direct CR writes are an administrator-only local acceptance/break-glass path,
not a new unauthenticated product configuration entry point.

## Formal apply runtime

The project metadata was scaffolded with kubebuilder v4.9.0, go/v4, project
version 3. It uses the repository's root module and existing controller-runtime
and Kubernetes versions; no nested module or Dockerfile is introduced.

The Manager watches CRs in `aep-system`. ModelRoute owns only per-route
Ingress/EnvoyFilter objects; DeploymentConfig owns aggregate plugins and the
quota-admin route. Routes wait for the aggregate's matching input digest.
On invalid/missing inputs they withdraw owned routing objects rather than
opening an anonymous or unconstrained route. Ready remains Unknown until
independent effectiveness observation is added in the integration stage.

Provider references resolve in `aep-credentials`. A metadata-only Secret watch
triggers aggregation; values are read through the uncached APIReader.
Native references resolve in `higress-system` and refresh on the bounded
30-second resync. Resync also detects rendered-resource deletion/drift.
API operations use a bounded reconciliation context.

SSA uses `aep-operator` without forced takeover. Existing objects must carry
the CR's owner UID label; unowned or foreign objects cause a conflict.
Cleanup queries live labels and uses UID/resourceVersion delete preconditions.
An apply/status crash or forged status inventory cannot authorize deletion
of another writer's objects. No cross-namespace OwnerReferences are used.
Replacements are installed before obsolete resources are pruned. Route
deletion waits for the aggregate without that route; deployment deletion
requires routes to be removed first. Invalid references retain a finalizer
until repaired, while route resources are withdrawn.

This stage does not adopt legacy reconciler resources or switch deployments.
Reviewed ownership handover, narrow RBAC revocation, outbox, DB-reference
validation and data-plane observation remain cutover prerequisites.

## Publication evidence

The opt-in publication relay stamps the immutable operation tuple in CR
annotations. Annotation-only republishes do not change generation. ModelRoute
and DeploymentConfig therefore copy the validated sequence, operationId,
revision and contentHash into `status.observedPublication` after handling the
input. Status patches use resourceVersion optimistic locking: a reconcile
started before a metadata/spec update cannot acknowledge the newer publication.
An incomplete marker clears the reference rather than reusing prior evidence.

ModelRoute also records the aggregate render digest in `status.appliedSnapshot`
after successful resource application. Control Service performs read-only,
bounded evidence reads for the latest projected operation. It verifies the
captured route specs/tombstones, all publication markers and ownership,
observed generations and matching aggregate/route digests. The aggregate is
read before and after the atomic route List; concurrent changes invalidate the
observation. A final deployment-scoped DB read rejects evidence superseded by
newer queued intent. Reads never acquire claims, mutate phases, resolve
credentials or write Kubernetes resources. Evidence is not a durable history
and is current only as of the observation; future changes can invalidate it.

Roll out the additive generated CRDs and operator before relying on the reader.
Old status or absent evidence stays Unknown. No additional Secret RBAC is
needed. Accepted/Applied describe validation/writes only; Ready remains Unknown
(`DataPlaneNotObserved`) until independent Higress/Envoy effectiveness evidence
and the Kind acceptance stage are complete. Default deployments stay unchanged.

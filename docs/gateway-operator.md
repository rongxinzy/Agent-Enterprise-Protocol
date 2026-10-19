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
- `ModelPolicy` selects a `ModelRoute` or the `DeploymentConfig` named
  `deployment`. An omitted subject defaults to model or global respectively.
  Global/user/team/role rules can span all models; a ModelRoute target narrows
  them to that model. Shared rules remain one native rule and one shared
  counter, never one counter per model. Subjects use existing trusted AEP
  authorizer headers and never grant model access.
- Relay-created policies carry `nativeRule.id/version` to preserve native
  Higress/Redis identity. Soft-deleted rules remain disabled CR tombstones;
  replaying older work cannot resurrect them. Direct policies can still use
  rateLimits and quota without a native identity.
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

The additive [effective model-access projection](model-access-grants.md) defines
`ModelAccessGrant` separately from rate/quota `ModelPolicy`. Its CRD is an
authorization contract only; publication, native enforcement and adoption
observations are separate stages and the default authorizer stays unchanged.

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

The operator fences partial relay delivery: every route and policy (including
disabled tombstones) must carry the same complete publication tuple and
deployment owner label as the aggregate before rendering begins. A child-first,
missing, foreign or mixed marker makes inputs unavailable; route controllers
withdraw owned ingress resources until the complete set converges. The last
aggregate plugins remain installed. This is fail-closed eventual convergence,
not an atomic xDS rollout or a guarantee of zero request interruption. Direct
administrator CR mode remains available only for an entirely unmarked set;
mixing direct and relay writers is rejected. Relay RBAC must remain exclusive.

## Formal apply runtime

The project metadata was scaffolded with kubebuilder v4.9.0, go/v4, project
version 3. It uses the repository's root module and existing controller-runtime
and Kubernetes versions; no nested module or Dockerfile is introduced.

The Manager watches CRs in `aep-system`. ModelRoute owns only per-route
Ingress/EnvoyFilter objects; DeploymentConfig owns aggregate plugins and the
quota-admin route. Routes wait for the aggregate's matching input digest.
Disabled routes have no serving ingress, so their model-scoped native rules
are omitted in both publication and direct-management modes. The ModelPolicy
is retained; re-enabling the route restores the same rule configuration.
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
First creation uses Kubernetes Create so a foreign object appearing after
the not-found read cannot be adopted. Subsequent SSA writes carry the verified
resourceVersion; an owner without a UID cannot write any rendered resource.
Client-go's `csaupgrade.UpgradeManagedFieldsPatch` promotes only the operator's
initial Update fields to Apply, retaining other managers and guarding the patch
with resourceVersion. It is idempotent after a crash between creation and SSA;
no forced field takeover or handwritten managedFields conversion is used.
Cleanup queries live labels and uses UID/resourceVersion delete preconditions.
An apply/status crash or forged status inventory cannot authorize deletion
of another writer's objects. No cross-namespace OwnerReferences are used.
Replacements are installed before obsolete resources are pruned. Route
deletion waits for the aggregate without that route; deployment deletion
requires routes to be removed first. Invalid references retain a finalizer
until repaired, while route resources are withdrawn.

This stage does not adopt legacy reconciler resources or switch deployments.

The opt-in deployment composition and explicit writer retirement are now in
`deploy/kubernetes/operator`; sensitive Envoy observation is separately selected
with `deploy/kubernetes/operator-observation`. See the
[handover and recovery runbook](gateway-operator-cutover.md) before applying.
Reviewed ownership handover, narrow RBAC revocation, outbox, DB-reference
validation and data-plane observation remain cutover prerequisites.

## Publication evidence

### DB reference lifecycle

Migration 048 atomically retires native limit rules when their user, team,
role or model is deleted. The database trigger uses the native writer's lock,
retains a disabled/versioned tombstone and records a coalescing invalidation
signal in the same transaction. Rollback removes both effects. Rule IDs stay
retired when a name-derived subject ID is reused. Existing orphaned references
are fenced by the migration backfill; review its write/lock cost before rollout.

When the opt-in relay is running, it consumes this signal before claiming
publication work. It copies only the latest selected immutable intent, disables
removed model routes and invalidated rules, and commits the new snapshot,
system-attributed audit and ordinary publication outbox together with the
signal acknowledgement. It never publishes unrelated drafts. Model removals
also retain the last selected sequence so deleting/recreating a model before
the worker wakes cannot reopen its predecessor's route. A later explicit
administrator publication can select the replacement model. Old idempotent
requests still replay their original operation; sequence fencing prevents
them from overwriting the newer withdrawal.

This is asynchronous removal, not immediate data-plane revocation. Existing
session/role authorization remains responsible for access revocation. It adds
no user/team model grants. A stopped relay retains pending signals and resumes
them after restart. Environments using the legacy publisher still require
explicit publication; they must not claim automatic Operator withdrawal.

The opt-in publication relay stamps the immutable operation tuple in CR
annotations. Annotation-only republishes do not change generation. ModelRoute
and DeploymentConfig therefore copy the validated sequence, operationId,
revision and contentHash into `status.observedPublication` after handling the
input. Status patches use resourceVersion optimistic locking: a reconcile
started before a metadata/spec update cannot acknowledge the newer publication.
An incomplete marker clears the reference rather than reusing prior evidence.

ModelRoute and ModelPolicy record the aggregate render digest in `status.appliedSnapshot`
after successful resource application. Control Service performs read-only,
bounded evidence reads for the latest projected operation. It verifies the
captured route specs/tombstones, all publication markers and ownership,
observed generations and matching aggregate/route digests. The aggregate is
read before and after the atomic route List; concurrent changes invalidate the
observation. A final deployment-scoped DB read rejects evidence superseded by
newer queued intent. Reads never acquire claims, mutate phases, resolve
credentials or write Kubernetes resources. Evidence is not a durable history
and is current only as of the observation; future changes can invalidate it.

`includeGatewayLimits: true` captures the route catalog and all native limit
drafts, including disabled/soft-deleted rules, in the same publication
transaction. Omitting it inherits the latest queued policy intent instead of
publishing unselected drafts or dropping existing policy. Reusing an idempotency
key never rereads drafts. The relay verifies policy specs/markers before
acknowledging delivery; the evidence reader checks their matching generation,
publication and aggregate digest. Missing or foreign policies fail closed.
Install the additive CRD schema, operator and ModelPolicy relay RBAC before
enabling combined publication. This does not activate the legacy limit publisher
or convert quota scopes into resource assignments.

Roll out the additive generated CRDs and operator before relying on the reader.
Old status or absent evidence stays Unknown. No additional Secret RBAC is
needed. Accepted/Applied describe validation/writes only. Independent observation
is opt-in as described below; default deployments keep Ready Unknown
(`DataPlaneNotObserved`).

### Independent Envoy observation (opt-in)

Install the additive generated CRDs first. After reviewing
`services/gateway-operator/config/observation/permissions.yaml`, apply its
namespace Role/RoleBinding and set `AEP_GATEWAY_OBSERVATION_DEPLOYMENT=higress-gateway`
on the operator. The named Deployment and Service must be in `higress-system`.
Rename both the setting and the named RBAC grants when using another name.
This package is deliberately separate from the default installation.

The operator uses client-go's standard Kubernetes port-forward transport to
read `/config_dump` from each serving gateway pod's loopback admin port 15000.
It never uses exec or opens a public admin listener. **Port-forward RBAC is
not restricted to read-only HTTP operations or port 15000.** Its holder can
reach other ports in pods in that namespace; grant it only to the dedicated
operator account after maintainer review. The implementation issues only GET,
disables redirects/proxies, limits responses to 16 MiB and bounds transport
and reconcile time. Dump bodies and resolved configuration exist only in
memory; neither status nor errors include them.

Observation requires a stable Deployment, matching ReplicaSet ownership,
all desired/updated/ready/available replicas, Ready pods and complete Service
EndpointSlice coverage. Foreign, draining, missing or changing endpoints
invalidate evidence. The fleet is read before and after collecting each
pod's dump; any inventory change discards the observation.

The supported topology is the Higress HTTP gateway Service port 80 targeting
port 80. The observer follows active Listeners through RDS or Higress Scoped
RDS (local-port/host key with the `80,*` scope), then checks wildcard virtual
hosts, both Prefix ingress routes, trusted model-header matches, upstream
cluster, host/path rewriting and injected headers. Official Envoy protobufs
normalize cluster, TLS and route fields. It compares exact ECDS plugin rules,
route matches, fail-open behavior and the pinned OCI artifact's loaded Wasm
filename digest. Warming resources, fallback RBAC filters and detached cache
entries cannot produce Ready. Disabled model/native-rule tombstones must have
no remaining routes, clusters or plugin entries. Unsupported topology stays
Unknown; this implementation is not a generic Envoy configuration verifier.

`status.dataPlane` stores only the snapshot digest, observation time, gateway
Deployment UID and covered pod UIDs. Rendering inputs are read directly from
the API before and after observation; changes to inputs, credentials or
publication markers discard the evidence. Child Ready conditions inherit only
matching fresh aggregate evidence. Control Service additionally requires exact
publication/spec/generation and matching Applied digests for every resource.
Evidence expires after 90 seconds and repeated success refreshes its own
timestamp, independently of condition transition times.

Ready means the selected configuration was observed loaded in the serving
fleet, not that an upstream provider is healthy or that every request succeeds.
It is point-in-time evidence with bounded freshness, not an atomic xDS update
or instantaneous revocation guarantee. Disabling the observer or revoking its
optional RBAC makes Ready Unknown and does not stop configuration publication.

For an explicitly selected isolated local cluster, a read-only integration
check is available (it is skipped in ordinary unit-test runs):

```bash
AEP_DATAPLANE_KUBECONFIG=<isolated-kind-kubeconfig> go test \
  ./services/gateway-operator/internal/controller \
  -run '^TestLocalKindLoadedConfiguration$' -count=1 -v
```

## Deployment package and isolated acceptance

`services/gateway-operator/config/default` is a Kustomize package for a fresh
operator environment. Install Higress core 2.2.4 in `higress-system` first.
The package installs the gateway CRDs, `aep-system` and `aep-credentials`, a
two-replica Manager, and namespace-scoped RoleBindings. It labels the AEP
namespaces with the restricted Pod Security standard; review existing
workloads before applying it to an existing namespace. It does not install
Higress, Redis or credentials, or modify the production baseline.

Build/apply from the root module using ko, with an approved non-root static
base image and deployment context:

```bash
export KO_DOCKER_REPO=<approved-registry>
ko apply -k services/gateway-operator/config/default
```

`make -C services/gateway-operator deploy` runs generation before the same
command. The deployment uses UID/GID 65532, a read-only root filesystem,
dropped capabilities, RuntimeDefault seccomp, probes and resource bounds.
No Dockerfile or provider credentials are embedded in the image.

The generated ClusterRole is permission inventory only: this package does
not bind it. Its Roles grant CR/status/finalizer updates in `aep-system`,
creation plus get/update of the named leader Lease, rendered-resource writes
only in `higress-system`, provider Secret reads/watches only in
`aep-credentials`, and get of the named native Secret `aep-native-gateway`
only. Kubernetes cannot restrict the name of a create operation; Lease create
is therefore namespace-scoped. Secret list/watch permission permits reading
all values in the credential namespace even though the implementation watches
metadata only; keep unrelated Secrets out of that namespace.

This package does **not** revoke old reconciler permissions, transfer object
ownership, or migrate DB configuration. Do not run it alongside a legacy
writer in an existing gateway. A reviewed cutover must stop the legacy writer,
revoke its Higress write grants and transfer/validate existing resources before
enabling the new operator. It must also provision the authenticated authorizer
path; an externally reachable gateway must not trust caller-supplied identity
headers. Direct fixture requests below are only for isolated acceptance.

The standalone, manually invoked test creates a dedicated Kind cluster and
installs real Higress/Envoy, Redis, mock providers and two operator replicas:

```bash
npm run test:e2e:gateway-operator
```

It builds the operator with pinned ko v0.18.0. `KIND`, `HELM` and
`AEP_OPERATOR_IMAGE` can select local tooling or a prebuilt operator image;
the default Kind node is pinned by digest and Higress core is pinned to 2.2.4.
`AEP_HIGRESS_CORE_CHART` can select an offline copy of that chart. Image export
selects linux/amd64 explicitly to avoid Docker's partially cached multiarch
indexes. `AEP_OPERATOR_E2E_REPORT` writes the successful check inventory.
This command is not yet wired into CI; existing CI/envtest and legacy Kind
tests remain in place and do not imply that this new acceptance ran.

The cluster name must start with `aep-operator-`. Existing clusters require
explicit `AEP_OPERATOR_E2E_REUSE=true`; reuse rejects a legacy reconciler.
New clusters and private temporary fixtures are removed on completion unless
`AEP_OPERATOR_E2E_KEEP=true`; reused clusters are preserved. Do not point the
test at a customer or preview cluster. Only disposable mock keys are used.

Acceptance checks cover namespace RBAC, OpenAI credential/model adaptation,
Anthropic streaming, native Redis limits for user × model, policy removal,
Lease leader replacement, final-route aggregate cleanup and deployment
finalizers preserving foreign objects. Requests cross actual Pod/Service
networking. They bypass AEP authentication with fixture headers and do not
test the public API, outbox, DB references, Prometheus, or production cutover.
Successful probes do not automatically update the CR's `Ready` condition.

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

The public `/aep/v1` API remains unchanged in this stage. Transactional config
outbox, explicit async API/SDK evolution, bounded cache consistency and DB
reference validation are required before routing Console writes to CRs.
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
Reviewed ownership handover, narrow RBAC revocation, outbox, DB-reference
validation and data-plane observation remain cutover prerequisites.

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

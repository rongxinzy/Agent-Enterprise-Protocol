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

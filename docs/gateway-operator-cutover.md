# Gateway operator handover and recovery

The opt-in `deploy/kubernetes/operator` target composes the production baseline,
CRDs, shared operator runtime, publication relay and credential publisher. The
legacy deployment has zero replicas and its three RoleBindings explicitly have
empty subjects; omitting resources from Kustomize output would not revoke live
grants. The legacy production default remains available for unmigrated sites.

This target is **not** an atomic or unattended handover command. Follow the
[reviewed handover and joint recovery procedure](gateway-operator-cutover.zh-CN.md).
Freeze management/inference traffic, drain all old writers, revoke every old
write grant, and verify ownership before removing approved legacy resources.
Install CRDs and await Established before custom resources. Never force-adopt
foreign objects or bypass finalizers without completing their cleanup duties.

Pin image digests and customize deployment ID, Redis, infrastructure Secret
store, database TLS and CNI API destinations. The publisher needs its own
read-only credentials/models database account and the control service's keyring.
Only one provider Secret delivery owner may be active. ESO still supplies
infrastructure keys; removing a provider ExternalSecret from output does not
stop its live controller or avoid Owner deletion side effects.

Control uses explicit operator mode even when delivery is paused. Recreate
avoids a mixed legacy/operator rolling window. Projected API tokens expire
after one hour; each process has its own namespace RBAC. Only the operator
writes Higress objects; control does not read/write Secret values. Optional
`operator-observation` grants sensitive pod port-forward access for full-fleet
Envoy verification. Without it, Applied cannot imply Ready.

Use `aepctl data-plane publication queue/get/replay`. Replay copies only the
latest selected immutable snapshot and preserves native rule identities, never
drafts. Reuse its explicit idempotency key on retry. A 202 response does not
prove delivery, apply or effectiveness. If DB is restored to T while CRs remain
at T+1, newer-sequence fencing must reject the old projection. Freeze writers
and restore a consistent approved DB/CR/Secret/Redis point or explicitly clean
only verified owned resources before rebuilding. Never bump a sequence or clear
markers to bypass the fence. Rollback also requires an exclusive-writer
handover and preparing legacy state from approved intent, not stale tables.

This migration covers gateway configuration. Compute assignments, business
defaults and digital employee lifecycle remain in their existing runtimes;
they are not speculative CRDs in this operator. Legacy compatibility code can
be removed separately after all deployments migrate and their rollback window
closes. No production rollout is performed by adding this package.

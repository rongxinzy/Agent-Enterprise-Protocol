# Data Plane Contract

The M3 data-plane contract separates the control plane's tenant-scoped desired state from the gateway reconciler's observed state.

`PUT /aep/v1/admin/data-plane/desired-state` accepts a deterministic `revision` and model routes. A route may refer to a Kubernetes Secret or external Secret by `name`, `namespace`, and `key`; the API never accepts or returns provider Secret values. Repeating the same revision is idempotent. A changed revision is the only supported way to publish a new state.

`GET /aep/v1/admin/data-plane/status` reports `pending`, `applying`, `ready`, `degraded`, or `error`, plus the observed revision, content hash, resource count, and a bounded failure message. The status is informational and does not grant access to Secret material.

PR #17 implements the reconciler that consumes this contract. It must apply only the desired revision, calculate a canonical SHA-256 content hash, use fail-closed credentials, and write status transitions without logging route secrets.

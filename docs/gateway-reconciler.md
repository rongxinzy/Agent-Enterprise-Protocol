# Gateway Reconciler

`services/gateway-reconciler` is a separate process from the control service. It polls the internal data-plane endpoint for each configured tenant, writes `applying` and `ready` or `error` status transitions, and atomically renders deterministic Higress resources into its output directory.

Required settings are `AEP_RECONCILER_CONTROL_URL`, `AEP_DATA_PLANE_RECONCILER_TOKEN`, and `AEP_RECONCILER_TENANTS`. The shared token is sent only in `X-AEP-Data-Plane-Token`; tenant identity is sent in `X-AEP-Tenant-ID`. Provider values are never accepted by the desired-state schema and are not emitted by the renderer.

The worker retries failed tenants with bounded exponential backoff. The output file is written through a temporary file and rename, so a consumer never observes a partially rendered document. It accepts `AEP_DATA_PLANE_RECONCILER_TOKEN_FILE` for a mounted Secret; the direct and file forms are mutually exclusive.

The production Kustomize baseline provides two hardened reconciler replicas, restricted Higress RBAC, TLS ingress, network policy, disruption budget, and External Secrets integration. When `AEP_RECONCILER_KUBERNETES_URL` and its service-account token are configured, the reconciler applies the deterministic Ingress and Higress WasmPlugin through Kubernetes server-side apply. Both replicas use the same field manager and desired object, so writes are idempotent and periodic convergence repairs drift. With no enabled routes, the owned Ingress is deleted and the plugin receives an empty match rule set. See [production-data-plane.md](production-data-plane.md).

# Trusted gateway metadata pipeline

Enable `AEP_GATEWAY_IDENTITY_URL=http://aep-control-service.aep-system.svc.cluster.local:8080/internal/gateway/identity`
on the authorizer, using the existing `AEP_GATEWAY_LICENSE_STATUS_TOKEN` Secret.
Each inference validates the active session/current model assignment and reads
all enabled team/role memberships from AEP. It does not cache these identities.
Lookups fail closed, so provision the control service for inference QPS.

The authorizer strips client AEP headers, overwrites `X-Mse-Consumer` with
`aep.<base64url(deployment-id)>.<base64url(user-id)>`, and emits team/role IDs
as sorted `|<base64url-id>|` entries. A forged `Connection` header cannot strip
these trusted headers. Production Higress inference listeners must be private
to the authorizer; do not expose a route accepting these headers directly.
The existing public entry path and internal Higress listener need separate
network boundaries if both use the same gateway deployment.

Apply the opt-in `deploy/kubernetes/monitoring/gateway-access-log.yaml` to add
a metadata-only Envoy stdout log. It captures the plugin's native `wasm.ai_log`
filter state and trusted identity headers, not model request/response bodies.
Keep ai-statistics default prompt/answer attributes disabled and logging below
debug level. Authorizer failures appear in its structured `gateway access`
events; pre-token failures have no invented user/team/role identity and are
only deployment-attributable on a dedicated authorizer with `AEP_DEPLOYMENT_ID`.

Merge `gateway-vector.yaml` into the existing Vector collector (validated with
Vector **0.49.0**) and wire `gateway_raw` only to the selected Higress/authorizer
containers. Set `AEP_LOKI_PUSH_URL` and source authentication in the deployment
Secret/configuration. The remap transform discards unknown fields, free-form
errors, model content and credentials. Native `unnest` copies one safe record
per call-time membership; it does no arithmetic. Membership labels in Loki
results are base64url IDs; resolve display names with existing AEP identity
APIs. The base stream is queried for whole-deployment or filtered totals, so
membership overlaps do not inflate those totals. Historical logs retain their
call-time membership even when a user later changes groups.

After verifying ingestion, enable `AEP_GATEWAY_ORGANIZATION_LOGS=true` on the
control service. Team/role metrics are evaluated by Loki's native range query
engine, not by AEP. Catalog `modelId` filters also use logs because Higress's
native `ai_model` can be an upstream model shared by several catalog aliases.
Without Loki that filter returns 503, never an incorrect alias subtotal.
Native log failure counts mean HTTP >=400; the Prometheus plugin's failure
counter means the plugin's detected model response failures. Responses carry
their native source; clients must retain that distinction in labels/help text.

Run the collector's built-in tests before deploying changes:

```bash
AEP_LOKI_PUSH_URL=http://loki:3100 vector test \
  deploy/kubernetes/monitoring/gateway-vector.yaml \
  deploy/kubernetes/monitoring/gateway-vector-tests.yaml
```

These overlays are deliberately not installed by the default production
kustomization. They integrate with the operator's existing collector/Loki and
do not create a second monitoring datastore or change retention implicitly.

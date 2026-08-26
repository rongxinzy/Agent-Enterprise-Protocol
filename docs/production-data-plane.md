# Production Data-plane Deployment

`deploy/kubernetes/production` is the production deployment baseline for the AEP control service, gateway authorizer, and gateway reconciler. It targets a Kubernetes cluster running the Higress Helm chart. `higress-standalone` is restricted to local Compose and CI fixtures.

## Prerequisites

- Kubernetes with the restricted Pod Security Standard enforced for `aep-system`.
- A pinned, production Higress Helm release in `higress-system`; apply [higress-values.yaml](../deploy/kubernetes/production/higress-values.yaml) after pinning the chart and image digests in the delivery system.
- cert-manager with a production `ClusterIssuer` named `production-acme`, or an equivalent certificate workflow that creates `aep-public-tls`.
- External Secrets Operator and a `ClusterSecretStore` named `production-secrets`. Map the illustrative remote keys in [external-secrets.yaml](../deploy/kubernetes/production/external-secrets.yaml) to the organization's Secret manager.
- Managed PostgreSQL and S3-compatible object storage. Update endpoint, issuer, tenant, hosts, image references, and resource limits through a reviewed overlay before rollout.

No Kubernetes `Secret` data, provider API key, signing seed, Credential keyring, or bootstrap password is stored in this repository. The control service and reconciler read the shared data-plane token from a mounted file, so it is not placed in a Pod environment value.

## Apply And Verify

Render first:

```sh
kubectl kustomize deploy/kubernetes/production
```

After image references, domains, Secret-store mappings, and external endpoints are approved, apply the baseline:

```sh
kubectl apply -k deploy/kubernetes/production
kubectl -n aep-system rollout status deployment/aep-control-service
kubectl -n aep-system rollout status deployment/aep-gateway-authorizer
kubectl -n aep-system rollout status deployment/aep-gateway-reconciler
```

The two reconciler replicas have independent audit volumes and a disruption budget. Both use Kubernetes server-side apply with the `aep-gateway-reconciler` field manager. Because every object name and body is deterministic, concurrent replicas have safe write ownership without a leader lease. A `ready` status is written only after both live Kubernetes operations succeed. Any partial failure reports `KUBERNETES_APPLY_FAILED` and is retried with bounded exponential backoff.

The reconciler Role and RoleBinding are deliberately scoped to Ingress and `extensions.higress.io/wasmplugins` in `higress-system`. The service-account token and cluster CA come from projected Kubernetes files. Validate the installed Higress CRD group and resource names before rollout.

Run `npm run test:e2e:m3-data-plane` for control-plane and fault convergence, and `npm run test:e2e:m3-kubernetes` for the real Kubernetes API Server and Higress-compatible CRD gate.

## DeepSeek Reasoning Routes

Set `providerType` explicitly in desired state when a route uses Higress' native DeepSeek provider. Legacy routes that omit it continue to use `openai`.

~~~json
{
  "revision": "models-2026-08-26",
  "routes": [{
    "modelId": "enterprise-reasoner",
    "enabled": true,
    "endpoint": "/v1/chat",
    "upstreamModel": "deepseek-reasoner",
    "protocol": "openai-compatible",
    "providerType": "deepseek",
    "credentialRef": {"name": "provider-secrets", "key": "deepseek-api-key", "namespace": "higress-system"}
  }]
}
~~~

The corresponding model descriptor should include the `reasoning` capability and `reasoningCompatibility` with `thinkingFormat: deepseek`. Clients must preserve streamed and non-streamed `reasoning_content`; when a tool-call conversation continues, they must replay the prior assistant `reasoning_content`. AEP still keeps inference on the direct gateway data path rather than tunneling it through SDK control APIs.

## Rollback

Use the prior immutable image digest only if it supports the current forward-only schema. Roll back manifests and Helm releases through the deployment system, then restore PostgreSQL, object storage, signing seed, and Credential keyring from one coordinated recovery point if schema compatibility is uncertain. Follow the detailed runtime runbook in [production-runtime.md](production-runtime.md).

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

The two reconciler replicas have independent audit volumes and a disruption budget. Kubernetes mode elects a leader through the `aep-system/gateway-reconciler-leader` Lease. Only a replica that successfully acquires and renews it synchronizes; followers wait for takeover. Production RBAC allows Lease creation in `aep-system`, but get/update only for this fixed Lease name, without access to other Leases. Read/create/renewal rejection clears leadership and retries instead of panicking or continuing synchronization. The leader uses Kubernetes server-side apply with the `aep-gateway-reconciler` field manager. A `ready` status is written only after all live Kubernetes operations succeed. Any partial failure reports `KUBERNETES_APPLY_FAILED` and is retried with bounded exponential backoff.

The reconciler Role and RoleBinding are deliberately scoped to Ingress, `extensions.higress.io/wasmplugins`, and `networking.istio.io/envoyfilters` in `higress-system`. The service-account token and cluster CA come from projected Kubernetes files. Validate the installed Higress CRD group and resource names before rollout.

Run `npm run test:e2e:m3-data-plane` for control-plane and fault convergence, and `npm run test:e2e:m3-kubernetes` for the real Kubernetes API Server and Higress-compatible CRD gate.

## AI Usage Statistics

The reconciler also applies `aep-ai-statistics-<deployment-suffix>`, using the
official open-source `ai-statistics` 2.0.1 OCI artifact pinned to
`sha256:9bebfc803f6ea92c0805670bd9a6e8a5bb727f2e1a86b133f20bb7002e71511e`.
Priority 200 runs observation before ai-proxy. `defaultConfigDisable: true`
restricts observation to the current deployment's enabled OpenAI and Anthropic
Ingress names; disabling every route leaves an empty match set. No global
plugin or unrelated gateway route is modified.

Lightweight response attributes collect model/usage metadata without enabling
full prompt, answer, tool-argument or reasoning attributes. `FAIL_OPEN` lets
inference continue if the observation plugin cannot load. Reconciler `ready`
confirms Kubernetes apply, not plugin loading or metrics collection; validate
real observations after rollout. The gateway must be able to fetch the pinned
OCI artifact, or the delivery system must mirror that exact artifact.

The gateway exposes Prometheus metrics at the internal endpoint
`http://<gateway-pod-ip>:15020/stats/prometheus`. Input/output Token counters,
request duration and streaming first-token duration come from Higress; values
depend on upstream usage reporting. Missing usage is not a zero-cost request.
This stage does not install Prometheus/Grafana, capture request content, add
user/team/role labels, calculate prices, or provide durable request logs.

Local verification uses the same pinned artifact in the isolated Compose
gateway fixture:

```sh
npm ci
npm run build --workspace @aep/sdk-node
npm run test:e2e:m1-gateway
```

The scenario checks actual exported counters for OpenAI non-streaming/SSE and
Anthropic non-streaming mock responses, preserves upstream 503 and authorization
behavior, and checks that credentials and model content do not enter metrics.
The container's metrics port stays internal. This is local evidence, not an
acceptance result for a production cluster or untested provider/protocol modes.

When rolling back to an older reconciler, remove its deployment-scoped
`aep-ai-statistics-<deployment-suffix>` object explicitly: older binaries do not
own or clear the newly introduced plugin. No database migration is involved.

To collect these metrics with an existing cluster Prometheus and import the
official dashboard into existing Grafana, follow [Higress monitoring](higress-monitoring.md).

## Catalog-Derived Publication

Prefer `POST /aep/v1/admin/data-plane/publish` over hand-written desired states. The model catalog is the single source of truth: the control service derives one route per `gateway` model with an OpenAI-compatible or Anthropic protocol and a complete endpoint and upstream model, and atomically replaces the desired state. Disabled models ride along as `enabled: false` routes so the reconciler deletes resources a route previously owned. This eliminates the structural drift where the catalog advertises a model but the gateway WasmPlugin has no matching `modelMapping`. Republishing an unchanged catalog is a no-op; any catalog change produces a new content-addressed revision.

Credential mapping is by convention. When a published model binds a Credential, its route references the Secret `aep-credential-<credentialId>` key `api-key` in `higress-system`. Provision one such Secret per referenced Credential through the deployment Secret system (for example an External Secret), with the provider key as the `api-key` value. The control service never writes Kubernetes and never emits Credential values; the reconciler reads the Secret at sync time and inlines the value into the rendered WasmPlugin. To rotate, rotate the Credential in the control plane and update the corresponding Secret; the next reconciliation picks up the new value. A route whose Secret is missing renders without an `apiToken`, and ai-proxy rejects its requests fail-closed. An anthropic passthrough route whose Secret is missing renders without the credential headers, and the upstream answers 401 itself — the failure surfaces upstream, not fail-closed at the gateway.

## Anthropic Passthrough Routes

A catalog model with `protocol: anthropic` publishes as an EnvoyFilter passthrough instead of an ai-proxy route: ai-proxy's Claude provider hard-codes `Host: api.anthropic.com` (Anthropic-protocol CDNs such as Zhipu BigModel answer 421) and its OpenAI provider remarshals every body. The reconciler renders, per model and without touching any shared resource (no McpBridge), an Ingress `aep-anthropic-<suffix>` under the client path prefix `/<sanitized-model-id>` plus an EnvoyFilter of the same name that adds a STRICT_DNS upstream cluster (TLS + SNI for https endpoints), redirects the route to it, rewrites the host, strips the model prefix in favor of the endpoint's own path, and injects the credential as `x-api-key` and `authorization` server-side.

Clients point an anthropic SDK at the gateway base URL plus the model prefix — for example `http://<gateway>/bench-anthropic` — and the SDK appends `/v1/messages`. The model body travels verbatim: there is no server-side model-id rewriting on this path, so send the model ID the token's scopes grant. Disabling the model (or deleting it and republishing) deletes the whole Ingress+EnvoyFilter pair on the next reconciliation.

**Deployment order matters**: roll out the reconciler image before the control service. An older reconciler does not know the `anthropic` protocol and silently renders such routes as OpenAI routes while reporting `ready`; the newer reconciler with an older control service is safe (it simply never sees an anthropic route). When retiring a hand-maintained passthrough route (for example the envsubst-rendered BigModel route in the governance repo), register the model, publish, verify `/<prefix>/v1/messages` through the new route, and only then delete the manual Ingress/EnvoyFilter — the two overlap safely because they own different path prefixes.

`GET /aep/v1/admin/data-plane/status` includes `catalogComparison`: catalog-publishable models missing from the desired routes (`missing`), desired routes the catalog would not publish (`extra`), and per-field mismatches (`mismatched`). Treat a non-empty comparison as drift to review; publish resolves it, or the manual `PUT` escape hatch intentionally maintains it (for example the native `deepseek` provider type below).

## DeepSeek Reasoning Routes

Set `providerType` explicitly in desired state when a route uses Higress' native DeepSeek provider. Legacy routes that omit it continue to use `openai`. Catalog-derived OpenAI-compatible routes always use `openai`, so a native DeepSeek route requires the manual escape hatch and will appear under `mismatched` until the catalog gains provider-type metadata.

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

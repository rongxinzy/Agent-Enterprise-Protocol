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

The two reconciler replicas have independent scratch volumes and a disruption budget. In this PR they still render deterministic Higress resources only; they do not apply Kubernetes resources. The next data-plane automation gate adds leader-safe live server-side apply and verifies it against Higress-compatible Kubernetes resources. Do not treat a `ready` status from this version as proof that a live Higress configuration changed.

The reconciler Role and RoleBinding are deliberately scoped to `higress-system` and are included so the next gate can use server-side apply without broad cluster permissions. Validate that the installed Higress CRD group and resource names match `extensions.higress.io/wasmplugins` before enabling that mode.

## Rollback

Use the prior immutable image digest only if it supports the current forward-only schema. Roll back manifests and Helm releases through the deployment system, then restore PostgreSQL, object storage, signing seed, and Credential keyring from one coordinated recovery point if schema compatibility is uncertain. Follow the detailed runtime runbook in [production-runtime.md](production-runtime.md).

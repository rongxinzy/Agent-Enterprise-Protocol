# Private quota management: native renderer stage

When the experimental native model ingress is enabled, its port-80 model
authentication also rejects the legitimate ai-quota management key. The AEP
quota API consequently returns `503 GATEWAY_SOURCE_UNAVAILABLE`.

The shared renderer now accepts `NativeGatewayConfig.PrivateQuotaListener`.
It defaults to false. This stage does **not** expose that setting in the
Operator, change the default topology, or complete issue #372. Runtime wiring,
namespaced RBAC, inventory/pruning and fleet observation are subsequent stages.
Do not manually enable these resources in production as a migration shortcut.

The opt-in renderer composes existing resources:

- An Istio v1 Gateway and VirtualService, and a ClusterIP Service on 18081.
- Domain-scoped native key-auth and ai-quota; inference retains the existing
  ingress-scoped quota configuration, Redis keys and Consumer convention.
- An Istio v1 AuthorizationPolicy DENY for the management host and port when
  Authorization is missing or differs from the management key. Other hosts
  and port 80 are outside that DENY rule.

VirtualService credential matching is insufficient on its own: ai-quota can
handle a request before the router. The native DENY is essential, including
when key-auth disappears. AEP does not implement another authenticator, quota
counter, balance calculation or cache.

## Reproduce the isolated native capability probe

Prepare the tools/images and Authorino schema from
[the managed native acceptance guide](access-trusted-quota.zh-CN.md). This
probe uses an owned random Kind cluster and Mock provider, never production.
Kind is a test dependency only.

Download the official [Istio 1.27.0 CRD bundle](https://github.com/istio/istio/blob/1.27.0/manifests/charts/base/files/crd-all.gen.yaml)
and select its actual schemas, without creating substitute definitions:

```sh
node scripts/prepare-private-quota-crds.mjs /tmp/crd-all.gen.yaml /tmp/private-quota-crds.json
AEP_NATIVE_ACCESS_BUSINESS=true \
AEP_NATIVE_ACCESS_HIGRESS=true \
AEP_NATIVE_ACCESS_MANAGED=true \
AEP_NATIVE_ACCESS_QUOTA_ADMIN=true \
AEP_NATIVE_ACCESS_QUOTA_PRIVATE_FIXTURE=true \
AEP_NATIVE_ACCESS_ISTIO_CRDS=/tmp/private-quota-crds.json \
AEP_NATIVE_ACCESS_POLICY_SOURCE_DIR=deploy/kubernetes/access-policy-source \
AEP_AUTHORINO_CRD_PATH=/path/to/authorino.kuadrant.io_authconfigs.yaml \
node tests/e2e/native-access-projection.mjs
```

Higress 2.2.4 discovery in this test consumes Istio 1.27 `v1` APIs. The older
1.19 Gateway/VirtualService CRDs do not serve v1: Kubernetes can store the
objects while discovery generates no listener. Both source and selected CRD
hashes are checked. This does not establish the API versions currently
installed in any production cluster.

The capability fixture is explicit test-side assembly, **not** the Operator
runtime applying the new renderer. It checks legitimate native and AEP
GET/refresh/delta, forged/missing/model credentials, alternate authorities,
key-auth removal, unchanged rejected balances, inference usage deduction,
zero-balance rejection and public-port isolation. The focused quota mode
does not execute the subsequent full session mutation/recovery baseline.

The local 21-check run completed and cleaned its cluster, with the missing
key-auth forgery rejected at 403. `productRendererImplemented` and
`networkPolicyEnforcementVerified` remain false in that capability report,
and `adoptionGate` remains false. These results do not prove production
network isolation, old-connection adoption, Redis-outage behavior, credential
publisher integration or full-fleet readiness. Envoy may retain unused ECDS
entries: removal checks target references in the active listener.

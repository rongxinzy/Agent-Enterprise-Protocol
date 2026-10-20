# Control-service rolling availability

The production control-service uses two replicas, `maxUnavailable: 0`, one
surge replica and a five-second readiness stability period. A native Kubernetes
`preStop.sleep` of five seconds keeps a terminating process serving while
EndpointSlice removal propagates, before SIGTERM starts the existing HTTP
shutdown. The 45-second termination budget includes that hook.

This protects identity discovery/JWKS as well as the control API during normal
rollouts. A valid JWT can otherwise be rejected by a cold native Authorino
evaluator when a JWKS fetch hits an already-stopped endpoint. Cached results do
not guarantee every process has a warm identity/key cache; cold-cache exceptions
remain part of the accepted design.

Use Kubernetes 1.34 or newer for stable native Sleep lifecycle support. Kubernetes
1.30–1.33 require `PodLifecycleSleepAction` enabled; validate older delivery
targets before applying this baseline. No shell or sleep binary is added to the
distroless image, and no custom JWT cache, verification retry or application
delay is introduced.
The version boundary is recorded in the upstream
[Kubernetes feature gates](https://github.com/kubernetes/kubernetes/blob/v1.35.0/pkg/features/kube_features.go).

The five-second intervals are bounded deployment defaults, not a promise that
all cluster networks converge in five seconds. Verify rollout under the target
CNI, load and DNS configuration; adjust the hook, readiness stability and grace
budget together in a reviewed delivery overlay. Readiness, liveness and
dependency checks retain their existing meanings. Abrupt node/network loss is
separate from graceful Pod replacement.

The isolated native access fixture uses these same rollout settings with real
control-service, PostgreSQL and native Authorino. It does not deploy to the
production cluster. See the [Chinese explanation](control-service-rollout.zh-CN.md).

The independent managed-business baseline passed 29 checks and cleaned its
owned cluster: role/team/user changes, session revocation, native request/token
limits, control outage and expiry, recovery, and Authorino replacement. A
separate integrated private-quota renderer probe passed 22 checks, including
first-request inference and exact Mock usage deduction after two control
rollouts. These are isolated test results, not production adoption evidence;
the native access adoption gate remains closed.

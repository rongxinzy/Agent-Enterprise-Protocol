#!/usr/bin/env bash
# Regenerate the operator's controller-gen artifacts (deepcopy, CRDs, RBAC).
# controller-gen is pinned to the controller-tools release whose k8s.io
# libraries match the root module's client-go (v0.37 line). Package paths are
# explicit because the ./... wildcard form does not recurse on every platform.
set -euo pipefail
cd "$(dirname "$0")"

CONTROLLER_GEN_VERSION="v0.22.0"
run() {
  go run "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION}" "$@"
}

run object paths=./api/v1alpha1
run crd paths=./api/v1alpha1 output:crd:artifacts:config=config/crd/bases
run rbac:roleName=gateway-operator-role paths=./internal/controller output:rbac:artifacts:config=config/rbac

#!/usr/bin/env bash
# Install pinned envtest binaries once per runner; pair with actions/cache on /tmp/envtest-assets.
set -euo pipefail
assets="/tmp/envtest-assets/controller-tools/envtest"
if [[ -d "$assets" ]]; then
  echo "KUBEBUILDER_ASSETS=$assets" >> "$GITHUB_ENV"
  exit 0
fi
mkdir -p /tmp/envtest-assets
curl -sfL --retry 3 --max-time 300 -o /tmp/envtest.tar.gz \
  https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v1.37.0/envtest-v1.37.0-linux-amd64.tar.gz
echo "1d1c453633b72c161a5d5a886cde7ac850be1a2ac796a9e1d4ffacacc64868295bdd2d57aa66cd0c158f5ce510f5dfe3fbc61ac21bd3dcfb875bd70658aa663a  /tmp/envtest.tar.gz" | sha512sum --check
tar -xzf /tmp/envtest.tar.gz -C /tmp/envtest-assets
echo "KUBEBUILDER_ASSETS=/tmp/envtest-assets/controller-tools/envtest" >> "$GITHUB_ENV"

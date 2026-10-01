#!/usr/bin/env bash
# Go coverage floor for the AEP monorepo (all services + aepctl).
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
min="${1:-80}"

coverprofile="$(mktemp -t cover.XXXXXX)"
trap 'rm -f "$coverprofile"' EXIT
go test ./... -coverpkg=./... -coverprofile="$coverprofile" >/dev/null

# -coverpkg writes one block per test binary; merge to the max hit.
merged="$(mktemp -t cover-merged.XXXXXX)"
python3 "$script_dir/merge-coverprofile.py" "$coverprofile" "$merged"
total="$(go tool cover -func="$merged" | awk '/^total:/ {sub(/%/, "", $3); print $3}')"
rm -f "$merged"

echo "AEP go coverage: ${total}% (floor ${min}%)"
if awk "BEGIN{exit !($total < $min)}"; then
  echo "::error::coverage ${total}% is below the ${min}% floor"
  exit 1
fi

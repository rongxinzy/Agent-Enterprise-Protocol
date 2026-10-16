#!/usr/bin/env bash
# Merge a Go coverprofile and print total coverage percentage (no trailing %).
set -euo pipefail
profile="$1"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
merged="$(mktemp -t cover-merged.XXXXXX)"
trap 'rm -f "$merged"' EXIT
python3 "$script_dir/merge-coverprofile.py" "$profile" "$merged"
go tool cover -func="$merged" | awk '/^total:/ {sub(/%/, "", $3); print $3}'

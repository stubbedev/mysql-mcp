#!/usr/bin/env bash
# Recompute the Go module vendorHash in flake.nix by building with a placeholder
# hash and reading the correct one out of Nix's mismatch error. Run from the
# repo root (or anywhere; it cd's to the repo root).
set -euo pipefail
cd "$(dirname "$0")/.."

placeholder="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
sed -i -E "s|vendorHash = \"sha256-[^\"]*\"|vendorHash = \"${placeholder}\"|" flake.nix

# Stage files so the flake evaluator (which reads the git tree) sees them.
git add -A

got="$(nix build .#default --no-link 2>&1 | grep -oE 'got: *sha256-[A-Za-z0-9+/=]+' | awk '{print $NF}' | tail -1 || true)"

if [ -z "${got}" ]; then
  echo "vendorHash already correct (build succeeded)."
  exit 0
fi

sed -i -E "s|vendorHash = \"sha256-[^\"]*\"|vendorHash = \"${got}\"|" flake.nix
echo "vendorHash updated to ${got}"

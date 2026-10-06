#!/usr/bin/env bash
# Build the TransferCLI release binaries and SHA256SUMS into dist/.
#   scripts/build-release.sh v2.0.0              # linux amd64 arm64 arm
#   scripts/build-release.sh v2.0.0 amd64        # only some architectures
# Needs Go >= the version in backend/go.mod and admin/go.mod. Used by .github/workflows/release.yml.
set -euo pipefail
VERSION="${1:?usage: $0 <version> [goarch...]}"; shift || true
ARCHES="${*:-amd64 arm64 arm}"
ROOT="$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)"
DIST="$ROOT/dist"
TSH_COMMIT="$(grep -oE 'github.com/dutchcoders/transfer.sh v[^ ]+' "$ROOT/backend/go.mod" | head -1 | grep -oE '[0-9a-f]{12}$' || true)"
[ -n "$TSH_COMMIT" ] || { echo "cannot read the transfer.sh commit from backend/go.mod" >&2; exit 1; }
TSH_VERSION="v1.6.1+${TSH_COMMIT:0:8} (transfercli ${VERSION})"
rm -rf "$DIST"; mkdir -p "$DIST"
for arch in $ARCHES; do
  export GOOS=linux GOARCH="$arch" CGO_ENABLED=0
  [ "$arch" = arm ] && export GOARM=7 || unset GOARM
  go build -C "$ROOT/admin"   -trimpath -ldflags "-s -w" -o "$DIST/transfercli-admin-linux-$arch" .
  go build -C "$ROOT/backend" -trimpath -ldflags "-s -w -X 'github.com/dutchcoders/transfer.sh/cmd.Version=$TSH_VERSION'" -o "$DIST/transfersh-linux-$arch" .
done
(cd "$DIST" && sha256sum transfercli-admin-linux-* transfersh-linux-* > SHA256SUMS)
cat "$DIST/SHA256SUMS"

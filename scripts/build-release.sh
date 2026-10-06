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
# Web UI: transfer.sh's built-in pages with the upload examples changed to send the upload password
# (TransferCLI requires one by default). __TC_UPLOAD_USER__ is replaced by install.sh.
WEB_TMP="$(mktemp -d)"; trap 'rm -rf "$WEB_TMP"' EXIT
unset GOOS GOARCH GOARM
go run -C "$ROOT/backend" ./cmd/webdump "$WEB_TMP/web"
for f in "$WEB_TMP/web/index.html" "$WEB_TMP/web/index.txt"; do
  # insert "-u USER:PASSWORD" after curl / curl.exe when the same command (up to the next pipe or line end) uploads
  perl -pi -e 's/\b(curl(?:\.exe)?) (?=[^\n|]*--upload-file)/$1 -u __TC_UPLOAD_USER__:PASSWORD /g' "$f"
done
n_html=$(grep -c -- '-u __TC_UPLOAD_USER__:PASSWORD' "$WEB_TMP/web/index.html" || true)
n_txt=$(grep -c -- '-u __TC_UPLOAD_USER__:PASSWORD' "$WEB_TMP/web/index.txt" || true)
# (grep exits 1 when nothing is left to patch; with pipefail that must not abort the build)
left=$( { grep -hE -- 'curl(\.exe)? --upload-file|curl -X PUT --upload-file|curl --progress-bar --upload-file' "$WEB_TMP/web/index.html" "$WEB_TMP/web/index.txt" || true; } | wc -l)
[ "$n_html" -gt 0 ] && [ "$n_txt" -gt 0 ] && [ "$left" -eq 0 ] || { echo "web UI patch failed (html=$n_html txt=$n_txt unpatched=$left)" >&2; exit 1; }
tar -C "$WEB_TMP" --owner=0 --group=0 --numeric-owner -czf "$DIST/transfercli-web.tar.gz" web
(cd "$DIST" && sha256sum transfercli-admin-linux-* transfersh-linux-* transfercli-web.tar.gz > SHA256SUMS)
cat "$DIST/SHA256SUMS"

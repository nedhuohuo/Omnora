#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
SOURCE="$ROOT/openapi/omnora.v1.yaml"
TARGET="$ROOT/internal/server/openapi_assets/omnora.v1.yaml"

usage() {
  cat <<'USAGE'
Usage: scripts/verification/sync-openapi-asset.sh [sync|--check]

The root OpenAPI document is the hand-maintained source. The embedded copy is
generated for the Go binary and must remain byte-for-byte identical.

  sync       Copy the root document into the embedded asset (default).
  --check    Fail and show a diff when the two files differ.
USAGE
}

[ -f "$SOURCE" ] || {
  printf 'FAIL: missing OpenAPI source: %s\n' "$SOURCE" >&2
  exit 1
}
[ -f "$TARGET" ] || {
  printf 'FAIL: missing embedded OpenAPI asset: %s\n' "$TARGET" >&2
  exit 1
}

ACTION=${1:-sync}
case "$ACTION" in
  sync)
    cp "$SOURCE" "$TARGET"
    printf 'synced: %s -> %s\n' "${SOURCE#"$ROOT/"}" "${TARGET#"$ROOT/"}"
    ;;
  --check)
    if cmp -s "$SOURCE" "$TARGET"; then
      printf 'ok: OpenAPI source and embedded asset are identical\n'
    else
      printf 'FAIL: OpenAPI source and embedded asset differ\n' >&2
      diff -u "$TARGET" "$SOURCE" || true
      exit 1
    fi
    ;;
  -h|--help)
    usage
    ;;
  *)
    printf 'FAIL: unknown action: %s\n' "$ACTION" >&2
    usage >&2
    exit 1
    ;;
esac

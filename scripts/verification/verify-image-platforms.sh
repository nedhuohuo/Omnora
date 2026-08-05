#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
PLATFORMS=${OMNORA_IMAGE_PLATFORMS:-linux/amd64,linux/arm64}
TAG=${OMNORA_IMAGE_TAG:-omnora:release-candidate}
OUTPUT=${OMNORA_IMAGE_OUTPUT:-type=cacheonly}
GOPROXY_VALUE=${GOPROXY:-https://proxy.golang.org,direct}

usage() {
  cat <<'USAGE'
Usage: scripts/verification/verify-image-platforms.sh

Builds the Docker image for the release target platforms with Docker buildx.
The default output is cache-only so the check proves the build without pushing.

Environment:
  OMNORA_IMAGE_PLATFORMS  default: linux/amd64,linux/arm64
  OMNORA_IMAGE_TAG        default: omnora:release-candidate
  OMNORA_IMAGE_OUTPUT     default: type=cacheonly
  GOPROXY                 default: https://proxy.golang.org,direct
USAGE
}

case "${1:-}" in
  -h|--help)
    usage
    exit 0
    ;;
  "")
    ;;
  *)
    printf 'FAIL: unknown argument: %s\n' "$1" >&2
    usage >&2
    exit 1
    ;;
esac

if ! command -v docker >/dev/null 2>&1; then
  printf 'FAIL: docker is required for multi-architecture image verification\n' >&2
  exit 1
fi

if ! docker buildx version >/dev/null 2>&1; then
  printf 'FAIL: docker buildx is required for multi-architecture image verification\n' >&2
  exit 1
fi

printf 'checking Docker buildx image for platforms: %s\n' "$PLATFORMS"
docker buildx build \
  --platform "$PLATFORMS" \
  --build-arg "GOPROXY=$GOPROXY_VALUE" \
  -t "$TAG" \
  --output "$OUTPUT" \
  "$ROOT"

printf 'ok: multi-architecture image build completed for %s\n' "$PLATFORMS"

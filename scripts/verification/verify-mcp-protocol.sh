#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
GOCACHE=${GOCACHE:-/private/tmp/omnora-go-cache}
GOMODCACHE=${GOMODCACHE:-/private/tmp/omnora-go-modcache}
export GOCACHE GOMODCACHE

step() {
  printf '\n==> %s\n' "$1"
}

step "Official SDK MCP client and raw protocol tests"
(
  cd "$ROOT"
  go test ./internal/mcpapi ./internal/server \
    -run 'MCP|Protocol|Transfer|DocumentationMatchesMCPContract' \
    -count=1
)

step "MCP catalogue and documentation contract"
"$ROOT/scripts/verification/verify-api-docs.sh"

printf '\nMCP protocol verification complete (deterministic; no external client or network)\n'

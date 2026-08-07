#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
STRICT=0

usage() {
  cat <<'USAGE'
Usage: scripts/verification/release-gate.sh [--strict]

Runs the local release gate for an Omnora release candidate.

Default mode runs checks that do not require external services:
  - go test ./...
  - explicit backup/restore Go test subset
  - web npm test -- --run
  - web npm build
  - static and Compose scaffold checks

Strict mode also requires:
  - multi-architecture Docker buildx verification
  - deployed HTTP health/readiness checks via OMNORA_DEPLOYED_BASE_URL
  - a completed NAS verification evidence file via OMNORA_NAS_VERIFICATION_RECORD
  - live MCP Inspector verification when OMNORA_MCP_URL and OMNORA_MCP_AI_TOKEN are supplied

Environment:
  GOCACHE                         default: /private/tmp/omnora-go-cache
  GOMODCACHE                      default: /private/tmp/omnora-go-modcache
  OMNORA_VERIFY_IMAGE_PLATFORMS   set to 1 to run image verification without --strict
  OMNORA_DEPLOYED_BASE_URL        deployed base URL, for example http://120.26.88.7:8080
  OMNORA_NAS_VERIFICATION_RECORD  path to a completed NAS evidence record
  OMNORA_MCP_URL                  live MCP /mcp endpoint for conditional Inspector smoke
  OMNORA_MCP_AI_TOKEN             short-lived scoped AI Token (never printed)
  OMNORA_MCP_INSPECTOR_CHECKLIST  completed Inspector checklist evidence path
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --strict)
      STRICT=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'FAIL: unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 1
      ;;
  esac
  shift
done

step() {
  printf '\n==> %s\n' "$1"
}

skip() {
  printf 'skip: %s\n' "$1"
}

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

GO_CACHE=${GOCACHE:-/private/tmp/omnora-go-cache}
GO_MOD_CACHE=${GOMODCACHE:-/private/tmp/omnora-go-modcache}

step "Go test suite"
GOCACHE="$GO_CACHE" GOMODCACHE="$GO_MOD_CACHE" go test ./...

step "Backup and restore rehearsal tests"
GOCACHE="$GO_CACHE" GOMODCACHE="$GO_MOD_CACHE" go test ./internal/store ./internal/server -run 'Backup|Restore'

step "Frontend test suite"
(cd "$ROOT/web" && npm test -- --run)

step "Frontend production build"
(cd "$ROOT/web" && npm run build)

step "OpenAPI, REST, and MCP documentation checks"
"$ROOT/scripts/verification/verify-api-docs.sh"

step "Deterministic MCP protocol and SDK checks"
"$ROOT/scripts/verification/verify-mcp-protocol.sh"

step "Static release scaffold and Compose checks"
if [ "$STRICT" -eq 1 ]; then
  OMNORA_REQUIRE_DOCKER_COMPOSE=1 "$ROOT/scripts/verification/verify-scaffolding.sh"
else
  "$ROOT/scripts/verification/verify-scaffolding.sh"
fi

if [ "${OMNORA_VERIFY_IMAGE_PLATFORMS:-0}" = "1" ] || [ "$STRICT" -eq 1 ]; then
  step "Multi-architecture image verification"
  "$ROOT/scripts/verification/verify-image-platforms.sh"
else
  skip "multi-architecture image verification; set OMNORA_VERIFY_IMAGE_PLATFORMS=1 or use --strict"
fi

if [ -n "${OMNORA_DEPLOYED_BASE_URL:-}" ]; then
  step "Deployed HTTP reachability"
  "$ROOT/scripts/verification/verify-deployed-http.sh" "$OMNORA_DEPLOYED_BASE_URL"
elif [ "$STRICT" -eq 1 ]; then
  fail "strict mode requires OMNORA_DEPLOYED_BASE_URL"
else
  skip "deployed HTTP reachability; set OMNORA_DEPLOYED_BASE_URL or use --strict"
fi

if [ -n "${OMNORA_MCP_URL:-}" ] || [ -n "${OMNORA_MCP_AI_TOKEN:-}" ]; then
  if [ -z "${OMNORA_MCP_URL:-}" ] || [ -z "${OMNORA_MCP_AI_TOKEN:-}" ]; then
    fail "OMNORA_MCP_URL and OMNORA_MCP_AI_TOKEN must be supplied together"
  fi
  if [ -z "${OMNORA_MCP_INSPECTOR_CHECKLIST:-}" ] || [ ! -f "$OMNORA_MCP_INSPECTOR_CHECKLIST" ]; then
    fail "live MCP Inspector verification requires a completed OMNORA_MCP_INSPECTOR_CHECKLIST file"
  fi
  inspector_template="$ROOT/docs/verification/mcp-inspector-checklist.md"
  expected_inspector_checks=$(grep -Ec '^[[:space:]]*-[[:space:]]+\[[xX ]\]' "$inspector_template")
  checked_inspector_checks=$(grep -Eoc '^[[:space:]]*-[[:space:]]+\[[xX]\]' "$OMNORA_MCP_INSPECTOR_CHECKLIST" || true)
  if [ "$checked_inspector_checks" -ne "$expected_inspector_checks" ]; then
    fail "OMNORA_MCP_INSPECTOR_CHECKLIST must contain all $expected_inspector_checks checked acceptance items"
  fi
  if grep -Eq '^[[:space:]]*-[[:space:]]+\[ \]' "$OMNORA_MCP_INSPECTOR_CHECKLIST"; then
    fail "OMNORA_MCP_INSPECTOR_CHECKLIST still contains unchecked acceptance items"
  fi
  grep -Eiq '^[[:space:]]*(status|状态)[[:space:]]*:[[:space:]]*(complete|completed|通过|已完成)[[:space:]]*$' "$OMNORA_MCP_INSPECTOR_CHECKLIST" \
    || fail "OMNORA_MCP_INSPECTOR_CHECKLIST needs an explicit complete status"
  grep -Eiq '^[[:space:]]*(redacted evidence|脱敏摘要)[[:space:]]*:[[:space:]]*[^[:space:]]' "$OMNORA_MCP_INSPECTOR_CHECKLIST" \
    || fail "OMNORA_MCP_INSPECTOR_CHECKLIST needs a non-empty redacted evidence summary"
  step "Live MCP Inspector modern smoke"
  "$ROOT/scripts/verification/verify-mcp-inspector.sh"
else
  skip "live MCP Inspector smoke; set OMNORA_MCP_URL, OMNORA_MCP_AI_TOKEN, and OMNORA_MCP_INSPECTOR_CHECKLIST"
fi

if [ -n "${OMNORA_NAS_VERIFICATION_RECORD:-}" ]; then
  step "NAS deployment evidence"
  "$ROOT/scripts/verification/verify-nas-record.sh" "$OMNORA_NAS_VERIFICATION_RECORD"
elif [ "$STRICT" -eq 1 ]; then
  fail "strict mode requires OMNORA_NAS_VERIFICATION_RECORD"
else
  skip "NAS deployment evidence; set OMNORA_NAS_VERIFICATION_RECORD or use --strict"
fi

printf '\nrelease gate complete\n'

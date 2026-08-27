#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
OPENAPI="$ROOT/openapi/omnora.v1.yaml"
OPENAPI_ASSET="$ROOT/internal/server/openapi_assets/omnora.v1.yaml"
OPENAPI_SYNC="$ROOT/scripts/verification/sync-openapi-asset.sh"

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

pass() {
  printf 'ok: %s\n' "$1"
}

for path in "$OPENAPI" "$OPENAPI_ASSET" "$OPENAPI_SYNC" \
  "$ROOT/docs/api/README.md" "$ROOT/docs/mcp/README.md" "$ROOT/README.md" "$ROOT/docs/README.md" \
  "$ROOT/docs/design/architecture.md" "$ROOT/docs/design/domain-model.md" \
  "$ROOT/docs/design/web-application-design.md" "$ROOT/docs/security/security-model.md" \
  "$ROOT/docs/requirements/product-requirements.md" "$ROOT/docs/verification/acceptance-criteria.md"; do
  [ -f "$path" ] || fail "missing documentation file: $path"
done
[ -f "$ROOT/docs/verification/mcp-inspector-checklist.md" ] || fail "missing MCP Inspector verification checklist"
[ -x "$OPENAPI_SYNC" ] || fail "OpenAPI sync script must be executable"
pass "API, MCP, security, product, and acceptance documentation files exist"

"$OPENAPI_SYNC" --check
grep -Eq '^openapi:[[:space:]]*3\.1\.0' "$OPENAPI" || fail "OpenAPI document must use 3.1.0"
grep -Fq '  /mcp:' "$OPENAPI" || fail "OpenAPI document must describe /mcp transport"
grep -Fq '  /mcp/transfers/{publicId}:' "$OPENAPI" || fail "OpenAPI must describe ticket download path"
grep -Fq '  /mcp/transfers/{publicId}/parts/{partNumber}:' "$OPENAPI" || fail "OpenAPI must describe ticket upload path"
grep -Fq 'ticketBearer:' "$OPENAPI" || fail "OpenAPI must define ticketBearer"
grep -Fq '2026-07-28' "$OPENAPI" || fail "OpenAPI must identify MCP 2026-07-28"
grep -Fq 'NOT IMPLEMENTED' "$OPENAPI" || fail "OpenAPI must state OAuth is NOT IMPLEMENTED"
if grep -Fq 'required: [method]' "$OPENAPI" || grep -Fq 'method-plus-params' "$OPENAPI"; then
  fail "OpenAPI must not advertise the obsolete private MCP envelope"
fi
if grep -Fq 'application/problem+json' "$OPENAPI"; then
  fail "OpenAPI must not advertise stale problem+json errors"
fi
grep -Fq '  - cookieSession: []' "$OPENAPI" || fail "REST OpenAPI default security must require the session cookie"
if grep -Eq '^  - bearerAuth:' "$OPENAPI"; then
  fail "Bearer authentication must not be a global REST security option"
fi
grep -Fq 'protected:' "$OPENAPI" || fail "OpenAPI User schema must retain protected"
grep -Fq 'initial_admin_protected' "$OPENAPI" || fail "OpenAPI must document initial admin protection errors"
grep -Fq 'mount_root_not_allowed' "$OPENAPI" || fail "OpenAPI must document mount root allowlist errors"
grep -Fq 'mount_not_writable' "$OPENAPI" || fail "OpenAPI must document mount not writable errors"
grep -Fq '  /member/content-sources:' "$OPENAPI" || fail "OpenAPI must describe member content sources"
grep -Fq '  /member/files/children:' "$OPENAPI" || fail "OpenAPI must describe member file children"
grep -Fq '"mounts:read"' "$OPENAPI" || fail "OpenAPI AI Token scopes must include mounts:read"
if grep -Fq '    DirectoryBoundary:' "$OPENAPI"; then
  fail "OpenAPI must not define the obsolete Space-scoped DirectoryBoundary"
fi
for source in all_account_content personal common_mount; do
  grep -Fq "const: $source" "$OPENAPI" || fail "OpenAPI AI Token boundary union is missing source: $source"
done
pass "OpenAPI transport, transfer, security, and error contracts are present"

GOCACHE=${GOCACHE:-/private/tmp/omnora-go-cache}
GOMODCACHE=${GOMODCACHE:-/private/tmp/omnora-go-modcache}
export GOCACHE GOMODCACHE
(
  cd "$ROOT"
  go test ./internal/mcpapi -run '^Test(DocumentationMatchesMCPContract|OpenAPIUsesAccountMountTokenContract)$' -count=1
)
pass "MCP catalogue and documentation contract test passed"

grep -Fq 'docs/api/README.md' "$ROOT/README.md" || fail "root README must link REST guide"
grep -Fq 'docs/mcp/README.md' "$ROOT/README.md" || fail "root README must link MCP guide"
grep -Fq 'api/README.md' "$ROOT/docs/README.md" || fail "docs README must link REST guide"
grep -Fq 'mcp/README.md' "$ROOT/docs/README.md" || fail "docs README must link MCP guide"
grep -Fq 'REST 资源 handler 当前要求会话 Cookie' "$ROOT/docs/api/README.md" || fail "REST guide must distinguish session Cookie"
grep -Fq 'Transfer Ticket' "$ROOT/docs/api/README.md" || fail "REST guide must distinguish transfer tickets"
grep -Fq 'MCP Wire Protocol：implemented and protocol-tested' "$ROOT/docs/verification/acceptance-criteria.md" || fail "acceptance labels missing"
pass "documentation navigation and authentication wording are aligned"

printf 'API and MCP documentation checks complete\n'

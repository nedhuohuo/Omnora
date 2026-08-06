#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
OPENAPI="$ROOT/openapi/omnora.v1.yaml"
OPENAPI_ASSET="$ROOT/internal/server/openapi_assets/omnora.v1.yaml"
OPENAPI_SYNC="$ROOT/scripts/verification/sync-openapi-asset.sh"
REST_DOC="$ROOT/docs/api/README.md"
MCP_DOC="$ROOT/docs/mcp/README.md"
ROOT_README="$ROOT/README.md"
DOCS_README="$ROOT/docs/README.md"

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

pass() {
  printf 'ok: %s\n' "$1"
}

for path in "$OPENAPI" "$OPENAPI_ASSET" "$OPENAPI_SYNC" "$REST_DOC" "$MCP_DOC" "$ROOT_README" "$DOCS_README"; do
  [ -f "$path" ] || fail "missing API documentation file: $path"
done
[ -x "$OPENAPI_SYNC" ] || fail "OpenAPI sync script must be executable"
pass "API and MCP documentation files exist"

"$OPENAPI_SYNC" --check

grep -Eq '^openapi:[[:space:]]*3\.1\.0' "$OPENAPI" || fail "OpenAPI document must use 3.1.0"
grep -Fq '  /mcp:' "$OPENAPI" || fail "OpenAPI document must describe /mcp"
if ! awk '
  /^  \/mcp:$/ { in_mcp = 1; next }
  in_mcp && /^  \/[^[:space:]]/ { in_mcp = 0 }
  in_mcp && /        - url: \// { found = 1 }
  END { exit(found ? 0 : 1) }
' "$OPENAPI"; then
  fail "OpenAPI MCP operation must use the runtime root server"
fi
grep -Fq 'required: [method]' "$OPENAPI" || fail "OpenAPI MCP request must require method"
grep -Fq 'required: [code, message, request_id]' "$OPENAPI" || fail "OpenAPI error schema must match the runtime envelope"
grep -Fq 'application/json:' "$OPENAPI" || fail "OpenAPI errors must use application/json"
grep -Fq '  - cookieSession: []' "$OPENAPI" || fail "REST OpenAPI default security must require the session cookie"
if grep -Eq '^  - bearerAuth:' "$OPENAPI"; then
  fail "Bearer authentication must not be a global REST security option"
fi
if grep -Fq 'application/problem+json' "$OPENAPI"; then
  fail "OpenAPI must not advertise the stale problem+json error envelope"
fi
grep -Fq 'protected:' "$OPENAPI" || fail "OpenAPI User schema must retain protected"
grep -Fq 'initial_admin_protected' "$OPENAPI" || fail "OpenAPI must document initial admin protection errors"
grep -Fq 'mount_root_not_allowed' "$OPENAPI" || fail "OpenAPI must document mount root allowlist errors"
grep -Fq 'AdminSpaceMember' "$OPENAPI" || fail "OpenAPI must describe protected admin space members"
grep -Fq 'MountDeletion' "$OPENAPI" || fail "OpenAPI must describe the admin mount deletion response"
pass "OpenAPI source reflects current MCP and runtime error contracts"

for method in tools/list spaces.list files.search files.list files.metadata files.read_text; do
  grep -Fq "$method" "$OPENAPI" || fail "OpenAPI MCP method missing: $method"
  grep -Fq "$method" "$MCP_DOC" || fail "MCP guide method missing: $method"
done
for scope in 'spaces:read' 'search:read' 'files:list' 'files:metadata' 'files:text'; do
  grep -Fq "$scope" "$MCP_DOC" || fail "MCP guide scope missing: $scope"
done
grep -Fq 'maxBytes' "$MCP_DOC" || fail "MCP guide must document maxBytes"
grep -Fq '1048576' "$MCP_DOC" || fail "MCP guide must document the 1 MiB text limit"
grep -Fq '不应描述为已经完成标准兼容的 Streamable HTTP MCP' "$MCP_DOC" || fail "MCP guide must distinguish current and target protocol status"
grep -Fq 'REST 资源 handler 仍要求会话 Cookie' "$MCP_DOC" || fail "MCP guide must state the current REST bearer boundary"
pass "MCP methods, scopes, limits, and protocol status are documented"

grep -Fq 'docs/api/README.md' "$ROOT_README" || fail "root README must link the REST guide"
grep -Fq 'docs/mcp/README.md' "$ROOT_README" || fail "root README must link the MCP guide"
grep -Fq 'api/README.md' "$DOCS_README" || fail "docs README must link the REST guide"
grep -Fq 'mcp/README.md' "$DOCS_README" || fail "docs README must link the MCP guide"
grep -Fq 'REST Bearer 资源访问尚未接入' "$REST_DOC" || fail "REST guide must state the current bearer boundary"
if grep -Fq 'Streamable HTTP MCP；' "$ROOT_README"; then
  fail "root README must not claim completed Streamable HTTP MCP"
fi
pass "documentation navigation and top-level protocol wording are aligned"

printf 'API and MCP documentation checks complete\n'

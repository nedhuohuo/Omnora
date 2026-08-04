#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
COMPOSE="$ROOT/deploy/docker-compose.yml"
DOCKERFILE="$ROOT/Dockerfile"
STATIC_INDEX="$ROOT/internal/server/static/index.html"
ALIYUN_TEST_COMPOSE="$ROOT/deploy/docker-compose.aliyun-test.yml"
ALIYUN_TEST_ENV_EXAMPLE="$ROOT/deploy/aliyun-test.env.example"
ALIYUN_TEST_DOC="$ROOT/docs/deployment/aliyun-test-server.md"
OPENAPI="$ROOT/openapi/omnora.v1.yaml"
CHECKLIST="$ROOT/scripts/verification/release-readiness-checklist.md"
GITIGNORE="$ROOT/.gitignore"

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

pass() {
  printf 'ok: %s\n' "$1"
}

[ -f "$COMPOSE" ] || fail "missing deploy/docker-compose.yml"
[ -f "$DOCKERFILE" ] || fail "missing Dockerfile"
[ -f "$STATIC_INDEX" ] || fail "missing embedded frontend placeholder"
[ -f "$ALIYUN_TEST_COMPOSE" ] || fail "missing deploy/docker-compose.aliyun-test.yml"
[ -f "$ALIYUN_TEST_ENV_EXAMPLE" ] || fail "missing deploy/aliyun-test.env.example"
[ -f "$ALIYUN_TEST_DOC" ] || fail "missing docs/deployment/aliyun-test-server.md"
[ -f "$OPENAPI" ] || fail "missing openapi/omnora.v1.yaml"
[ -f "$CHECKLIST" ] || fail "missing scripts/verification/release-readiness-checklist.md"
[ -f "$GITIGNORE" ] || fail "missing .gitignore"
pass "expected scaffold files exist"

grep -Fq 'OMNORA_TOTP_ENCRYPTION_KEY' "$COMPOSE" ||
  fail "compose file must inject the TOTP encryption key"
grep -Fq 'COPY --from=web-build' "$DOCKERFILE" ||
  fail "Dockerfile must build and embed the frontend bundle"
grep -Fq 'OMNORA_TEST_SERVER_SSH_PORT=22' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must use the current SSH port"
pass "deployment image and current Aliyun SSH settings are wired"

grep -Eq '^services:' "$COMPOSE" || fail "compose file must declare services"
grep -Eq '^  omnora:' "$COMPOSE" || fail "compose file must declare a single omnora service"
service_count=$(awk '
  /^services:/ { in_services=1; next }
  in_services && /^[^[:space:]#]/ { in_services=0 }
  in_services && /^  [A-Za-z0-9_-]+:/ { count++ }
  END { print count + 0 }
' "$COMPOSE")
[ "$service_count" -eq 1 ] || fail "compose file must contain exactly one service"
pass "compose file contains exactly one omnora service"

if grep -Eiq 'postgres|redis|opensearch|minio|onlyoffice|libreoffice|ffmpeg|docker\.sock|network_mode:[[:space:]]*host|privileged:[[:space:]]*true' "$COMPOSE"; then
  fail "compose file contains a first-version non-goal or unsafe container setting"
fi
pass "compose file avoids external service dependencies and privileged settings"

if grep -Eiq 'docker\.sock|network_mode:[[:space:]]*host|privileged:[[:space:]]*true|:rw([[:space:]#]|$)' "$ALIYUN_TEST_COMPOSE"; then
  fail "Aliyun test compose contains unsafe privileged or write-mount setting"
fi
grep -Fq 'omnora.environment: aliyun-test' "$ALIYUN_TEST_COMPOSE" ||
  fail "Aliyun test compose must label the deployment environment"
grep -Fq 'OMNORA_PROXY_BIND:-127.0.0.1' "$ALIYUN_TEST_COMPOSE" ||
  fail "Aliyun test compose must keep proxy_https host-local by default"
grep -Fq 'OMNORA_LAN_BIND:-0.0.0.0' "$ALIYUN_TEST_COMPOSE" ||
  fail "Aliyun test compose must default LAN HTTP bind to 0.0.0.0 for external test access"
grep -Fq 'OMNORA_TEST_SERVER_HOST=replace-with-private-vault-host' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must not commit the real host"
grep -Fq 'OMNORA_LAN_BIND=0.0.0.0' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must expose LAN HTTP on 0.0.0.0"
grep -Fq 'OMNORA_PROXY_BIND=127.0.0.1' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must keep proxy_https on 127.0.0.1"
grep -Fq 'OMNORA_ROUTE_MCP_ENABLED=false' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must keep MCP disabled by default"
grep -Fq 'OMNORA_ROUTE_SHARE_ENABLED=false' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must keep sharing disabled by default"
grep -Fq 'deploy/*.env' "$GITIGNORE" ||
  fail ".gitignore must exclude real deployment env files"
grep -Fq 'deploy/aliyun-test/' "$GITIGNORE" ||
  fail ".gitignore must exclude Aliyun test runtime data"
grep -Fq 'http://120.26.88.7:8080' "$ALIYUN_TEST_DOC" ||
  fail "Aliyun test deployment doc must document the external HTTP URL"
grep -Fq 'OMNORA_LAN_BIND=0.0.0.0' "$ALIYUN_TEST_DOC" ||
  fail "Aliyun test deployment doc must document the public LAN bind"
grep -Fq 'OMNORA_DEPLOY_ENV=aliyun-test' "$CHECKLIST" ||
  fail "release checklist must state deploy env is not a security boundary"
pass "Aliyun test-server scaffold documents external HTTP access with local proxy_https"

grep -Eq '^openapi:[[:space:]]*3\.1\.0' "$OPENAPI" || fail "OpenAPI document must use 3.1.0"
for route_group in admin_web member_web share rest mcp openapi; do
  grep -Eq "^[[:space:]]*-[[:space:]]*$route_group$|x-omnora-route-group:[[:space:]]*$route_group" "$OPENAPI" ||
    fail "OpenAPI document must mention route group $route_group"
done
pass "OpenAPI document names first-version route groups"

for path in \
  '/spaces' \
  '/spaces/{spaceId}/mounts' \
  '/spaces/{spaceId}/search' \
  '/files/{objectId}' \
  '/files/{objectId}/text' \
  '/files/{objectId}/download-ticket' \
  '/uploads' \
  '/shares' \
  '/share-sessions' \
  '/ai-tokens' \
  '/admin/mounts' \
  '/mcp'
do
  grep -Fq "$path" "$OPENAPI" || fail "OpenAPI document missing core path $path"
done
pass "OpenAPI document includes core first-version endpoint groups"

grep -Fq 'mount_identity_unverifiable' "$OPENAPI" || fail "OpenAPI document must include mount_identity_unverifiable"
grep -Fq 'route_group_disabled' "$OPENAPI" || fail "OpenAPI document must include route_group_disabled"
grep -Fq 'fragmentSecret' "$OPENAPI" || fail "OpenAPI document must model share fragment secret exchange"
pass "OpenAPI document includes key stable security/error semantics"

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  docker compose -f "$COMPOSE" config --quiet
  pass "docker compose config validates locally"
  docker compose --env-file "$ALIYUN_TEST_ENV_EXAMPLE" -f "$COMPOSE" -f "$ALIYUN_TEST_COMPOSE" config --quiet
  pass "Aliyun test compose override validates locally"
else
  printf 'skip: docker compose is not available; static compose checks passed\n'
fi

printf 'verification scaffold checks complete\n'

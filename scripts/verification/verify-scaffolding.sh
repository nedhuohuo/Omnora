#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
REQUIRE_COMPOSE=${OMNORA_REQUIRE_DOCKER_COMPOSE:-0}
COMPOSE="$ROOT/deploy/docker-compose.yml"
NAS_COMPOSE="$ROOT/deploy/docker-compose.nas.yml"
DOCKERFILE="$ROOT/Dockerfile"
ENTRYPOINT="$ROOT/deploy/docker-entrypoint.sh"
ENTRYPOINT_TEST="$ROOT/scripts/verification/test-docker-entrypoint.sh"
STATIC_INDEX="$ROOT/internal/server/static/index.html"
ALIYUN_TEST_COMPOSE="$ROOT/deploy/docker-compose.aliyun-test.yml"
ALIYUN_TEST_ENV_EXAMPLE="$ROOT/deploy/aliyun-test.env.example"
ALIYUN_TEST_DOC="$ROOT/docs/deployment/aliyun-test-server.md"
OPENAPI="$ROOT/openapi/omnora.v1.yaml"
CHECKLIST="$ROOT/scripts/verification/release-readiness-checklist.md"
GITIGNORE="$ROOT/.gitignore"
RELEASE_GATE="$ROOT/scripts/verification/release-gate.sh"
IMAGE_CHECK="$ROOT/scripts/verification/verify-image-platforms.sh"
HTTP_CHECK="$ROOT/scripts/verification/verify-deployed-http.sh"
NAS_RECORD_CHECK="$ROOT/scripts/verification/verify-nas-record.sh"
NAS_VERIFICATION_DOC="$ROOT/docs/deployment/nas-verification.md"
API_DOCS_CHECK="$ROOT/scripts/verification/verify-api-docs.sh"
PUBLISH_WORKFLOW="$ROOT/.github/workflows/publish-image.yml"

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

pass() {
  printf 'ok: %s\n' "$1"
}

[ -f "$COMPOSE" ] || fail "missing deploy/docker-compose.yml"
[ -f "$NAS_COMPOSE" ] || fail "missing deploy/docker-compose.nas.yml"
[ -f "$DOCKERFILE" ] || fail "missing Dockerfile"
[ -f "$ENTRYPOINT" ] || fail "missing deploy/docker-entrypoint.sh"
[ -x "$ENTRYPOINT_TEST" ] || fail "missing executable Docker entrypoint regression test"
[ -f "$STATIC_INDEX" ] || fail "missing embedded frontend placeholder"
[ -f "$ALIYUN_TEST_COMPOSE" ] || fail "missing deploy/docker-compose.aliyun-test.yml"
[ -f "$ALIYUN_TEST_ENV_EXAMPLE" ] || fail "missing deploy/aliyun-test.env.example"
[ -f "$ALIYUN_TEST_DOC" ] || fail "missing docs/deployment/aliyun-test-server.md"
[ -f "$OPENAPI" ] || fail "missing openapi/omnora.v1.yaml"
[ -f "$CHECKLIST" ] || fail "missing scripts/verification/release-readiness-checklist.md"
[ -f "$GITIGNORE" ] || fail "missing .gitignore"
[ -x "$RELEASE_GATE" ] || fail "missing executable scripts/verification/release-gate.sh"
[ -x "$IMAGE_CHECK" ] || fail "missing executable scripts/verification/verify-image-platforms.sh"
[ -x "$HTTP_CHECK" ] || fail "missing executable scripts/verification/verify-deployed-http.sh"
[ -x "$NAS_RECORD_CHECK" ] || fail "missing executable scripts/verification/verify-nas-record.sh"
[ -f "$NAS_VERIFICATION_DOC" ] || fail "missing docs/deployment/nas-verification.md"
[ -x "$API_DOCS_CHECK" ] || fail "missing executable scripts/verification/verify-api-docs.sh"
[ -f "$PUBLISH_WORKFLOW" ] || fail "missing .github/workflows/publish-image.yml"
pass "expected scaffold files exist"

"$API_DOCS_CHECK"
pass "OpenAPI, REST, and MCP documentation checks"

grep -Fq 'OMNORA_TOTP_ENCRYPTION_KEY' "$COMPOSE" ||
  fail "compose file must inject the TOTP encryption key"
grep -Fq 'OMNORA_TOTP_ENCRYPTION_KEY: "${OMNORA_TOTP_ENCRYPTION_KEY:-}"' "$COMPOSE" ||
  fail "compose file must allow the entrypoint to generate a missing TOTP key"
grep -Fq 'OMNORA_TOTP_ENCRYPTION_KEY="$(random_hex)"' "$ENTRYPOINT" ||
  fail "docker entrypoint must generate a missing TOTP key"
grep -Fq 'OMNORA_LOG_FORMAT' "$COMPOSE" ||
  fail "compose file must expose log format configuration"
grep -Fq 'OMNORA_LOG_LEVEL' "$COMPOSE" ||
  fail "compose file must expose log level configuration"
grep -Fq 'driver: local' "$COMPOSE" ||
  fail "compose file must configure bounded local container logs"
grep -Fq 'COPY --from=web-build' "$DOCKERFILE" ||
  fail "Dockerfile must build and embed the frontend bundle"
grep -Fq 'OMNORA_TEST_SERVER_SSH_PORT=22' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must use the current SSH port"
grep -Fq 'OMNORA_LOG_FORMAT=json' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must default to JSON logs"
pass "deployment image and current Aliyun SSH settings are wired"

"$ENTRYPOINT_TEST"
pass "Docker entrypoint preserves persistent instance state"

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

grep -Fq './managed:/srv/omnora/managed:rw' "$COMPOSE" ||
  fail "base compose must keep managed storage writable"
grep -Fq './mounts:/mnt/omnora:rw' "$COMPOSE" ||
  fail "base compose must keep the predeclared mount root writable"
grep -Fq './managed:/srv/omnora/managed:rw' "$NAS_COMPOSE" ||
  fail "NAS compose must keep managed storage writable"
grep -Fq './mounts:/mnt/omnora:rw' "$NAS_COMPOSE" ||
  fail "NAS compose must keep the predeclared mount root writable"
grep -Fq 'image: "${OMNORA_IMAGE:?set OMNORA_IMAGE' "$NAS_COMPOSE" ||
  fail "NAS compose must require an explicitly verified image tag or digest"
pass "default managed and predeclared mount roots are writable"

if grep -Eiq 'docker\.sock|network_mode:[[:space:]]*host|privileged:[[:space:]]*true' "$ALIYUN_TEST_COMPOSE"; then
  fail "Aliyun test compose contains unsafe privileged setting"
fi
grep -Fq './aliyun-test/managed:/srv/omnora/managed:rw' "$ALIYUN_TEST_COMPOSE" ||
  fail "Aliyun test compose must keep managed storage writable"
grep -Fq './aliyun-test/mounts:/mnt/omnora:rw' "$ALIYUN_TEST_COMPOSE" ||
  fail "Aliyun test compose must keep the predeclared mount root writable"
grep -Fq 'omnora.environment: aliyun-test' "$ALIYUN_TEST_COMPOSE" ||
  fail "Aliyun test compose must label the deployment environment"
grep -Fq 'OMNORA_BIND=0.0.0.0' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must default HTTP bind to 0.0.0.0 for external test access"
grep -Fq 'OMNORA_TEST_SERVER_HOST=replace-with-private-vault-host' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must not commit the real host"
grep -Fq 'OMNORA_BIND=0.0.0.0' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must expose HTTP on 0.0.0.0"
grep -Fq 'OMNORA_ROUTE_MCP_ENABLED=false' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must keep MCP disabled by default"
grep -Fq 'OMNORA_ROUTE_SHARE_ENABLED=true' "$ALIYUN_TEST_ENV_EXAMPLE" ||
  fail "Aliyun test env example must enable sharing for QA share-link coverage"
grep -Fq 'Share route group is enabled in the Aliyun QA env example' "$CHECKLIST" ||
  fail "release checklist must match the Aliyun QA share-route default"
grep -Fq 'OMNORA_ROUTE_SHARE_ENABLED=true' "$ALIYUN_TEST_DOC" ||
  fail "Aliyun test deployment doc must match the QA share-route env default"
grep -Fq 'deploy/*.env' "$GITIGNORE" ||
  fail ".gitignore must exclude real deployment env files"
grep -Fq 'deploy/aliyun-test/' "$GITIGNORE" ||
  fail ".gitignore must exclude Aliyun test runtime data"
grep -Fq 'http://120.26.88.7:8080' "$ALIYUN_TEST_DOC" ||
  fail "Aliyun test deployment doc must document the external HTTP URL"
grep -Fq 'OMNORA_BIND=0.0.0.0' "$ALIYUN_TEST_DOC" ||
  fail "Aliyun test deployment doc must document the public HTTP bind"
grep -Fq 'OMNORA_DEPLOY_ENV=aliyun-test' "$CHECKLIST" ||
  fail "release checklist must state deploy env is not a security boundary"
pass "Aliyun test-server scaffold documents external HTTP access"

grep -Fq 'go test ./...' "$RELEASE_GATE" ||
  fail "release gate must run the Go test suite"
grep -Fq 'npm test -- --run' "$RELEASE_GATE" ||
  fail "release gate must run the frontend test suite"
grep -Fq 'npm run build' "$RELEASE_GATE" ||
  fail "release gate must run the frontend build"
grep -Fq 'verify-scaffolding.sh' "$RELEASE_GATE" ||
  fail "release gate must include scaffold checks"
grep -Fq 'verify-api-docs.sh' "$RELEASE_GATE" ||
  fail "release gate must include API documentation checks"
grep -Fq 'release-gate.sh' "$PUBLISH_WORKFLOW" ||
  fail "image publication must run the release gate before building"
grep -Fq 'linux/amd64,linux/arm64' "$IMAGE_CHECK" ||
  fail "image verification must target amd64 and arm64 by default"
grep -Fq '/healthz' "$HTTP_CHECK" ||
  fail "deployed HTTP verification must check health"
grep -Fq '/readyz' "$HTTP_CHECK" ||
  fail "deployed HTTP verification must check readiness"
grep -Fq 'X-Request-ID' "$HTTP_CHECK" ||
  fail "deployed HTTP verification must check request ID correlation"
grep -Fq 'zspace_nas: PASS' "$NAS_VERIFICATION_DOC" ||
  fail "NAS verification doc must include machine-readable zspace evidence marker"
grep -Fq 'generic_linux_compose: PASS' "$NAS_VERIFICATION_DOC" ||
  fail "NAS verification doc must include machine-readable generic Linux evidence marker"
grep -Fq 'Failed request correlation by request ID' "$NAS_VERIFICATION_DOC" ||
  fail "NAS verification doc must require request ID log correlation evidence"
pass "release gate scripts and NAS verification template are wired"

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
elif [ "$REQUIRE_COMPOSE" = "1" ]; then
  fail "docker compose is required when OMNORA_REQUIRE_DOCKER_COMPOSE=1"
else
  printf 'skip: docker compose is not available; static compose checks passed\n'
fi

printf 'verification scaffold checks complete\n'

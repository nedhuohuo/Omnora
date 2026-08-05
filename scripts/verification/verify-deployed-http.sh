#!/usr/bin/env sh
set -eu

BASE_URL=${1:-${OMNORA_DEPLOYED_BASE_URL:-}}
TIMEOUT=${OMNORA_HTTP_CHECK_TIMEOUT:-8}

usage() {
  cat <<'USAGE'
Usage: scripts/verification/verify-deployed-http.sh <base-url>

Checks an already deployed Omnora endpoint from the caller's network.

Required checks:
  - GET /healthz returns JSON status ok
  - GET /readyz returns JSON status ready
  - GET / returns a browser page
  - X-Request-ID is preserved for proxy/application log correlation

Optional checks:
  - set OMNORA_EXPECT_OPENAPI=1 to require /openapi/omnora.v1.yaml

Environment:
  OMNORA_HTTP_CHECK_TIMEOUT  curl max-time seconds, default: 8
  OMNORA_EXPECT_OPENAPI      set to 1 to require OpenAPI reachability
USAGE
}

case "$BASE_URL" in
  -h|--help)
    usage
    exit 0
    ;;
  "")
    printf 'FAIL: base URL is required\n' >&2
    usage >&2
    exit 1
    ;;
esac

if ! command -v curl >/dev/null 2>&1; then
  printf 'FAIL: curl is required for deployed HTTP verification\n' >&2
  exit 1
fi

BASE_URL=${BASE_URL%/}

fetch() {
  path=$1
  curl -fsS --max-time "$TIMEOUT" "$BASE_URL$path"
}

assert_json_status() {
  path=$1
  expected=$2
  body=$(fetch "$path")
  printf '%s' "$body" | grep -Eq '"status"[[:space:]]*:[[:space:]]*"'"$expected"'"' ||
    {
      printf 'FAIL: %s did not return status %s; body: %s\n' "$path" "$expected" "$body" >&2
      exit 1
    }
  printf 'ok: %s status %s\n' "$path" "$expected"
}

assert_json_status "/healthz" "ok"
assert_json_status "/readyz" "ready"

request_id="omnora-http-check"
headers=$(curl -fsS -D - -o /dev/null --max-time "$TIMEOUT" -H "X-Request-ID: $request_id" "$BASE_URL/readyz")
printf '%s' "$headers" | grep -Eiq '^X-Request-ID:[[:space:]]*'"$request_id"'[[:space:]]*\r?$' ||
  {
    printf 'FAIL: /readyz did not preserve X-Request-ID for log correlation; headers: %s\n' "$headers" >&2
    exit 1
  }
printf 'ok: X-Request-ID preserved\n'

root_body=$(fetch "/")
printf '%s' "$root_body" | grep -Eiq '<html|Omnora|万境' ||
  {
    printf 'FAIL: / did not look like a browser page\n' >&2
    exit 1
  }
printf 'ok: / browser entry reachable\n'

if [ "${OMNORA_EXPECT_OPENAPI:-0}" = "1" ]; then
  openapi_body=$(fetch "/openapi/omnora.v1.yaml")
  printf '%s' "$openapi_body" | grep -Eq '^openapi:[[:space:]]*3\.1\.0' ||
    {
      printf 'FAIL: /openapi/omnora.v1.yaml did not expose OpenAPI 3.1.0\n' >&2
      exit 1
    }
  printf 'ok: OpenAPI document reachable\n'
fi

printf 'ok: external reachability verified for %s\n' "$BASE_URL"

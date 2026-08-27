#!/usr/bin/env sh
set -eu

if [ -z "${OMNORA_MCP_URL:-}" ]; then
  printf 'FAIL: OMNORA_MCP_URL is required\n' >&2
  exit 1
fi
if [ -z "${OMNORA_MCP_AI_TOKEN:-}" ]; then
  printf 'FAIL: OMNORA_MCP_AI_TOKEN is required\n' >&2
  exit 1
fi

INIT_JSON=$(mktemp "${TMPDIR:-/tmp}/omnora-mcp-init.XXXXXX")
TOOLS_JSON=$(mktemp "${TMPDIR:-/tmp}/omnora-mcp-tools.XXXXXX")
CLI_STDERR=$(mktemp "${TMPDIR:-/tmp}/omnora-mcp-inspector.XXXXXX")
trap 'rm -f "$INIT_JSON" "$TOOLS_JSON" "$CLI_STDERR"' EXIT HUP INT TERM

# Do not enable shell tracing and do not print either credential. The token is
# intentionally passed as a process argument because this is an explicit local
# Inspector acceptance check; the checklist documents that process-table risk.
run_inspector() {
  output=$1
  method=$2
  if ! npx --yes @modelcontextprotocol/inspector@2.1.0 --cli \
    "$OMNORA_MCP_URL" \
    --transport http \
    --method "$method" \
    --header "Authorization: Bearer $OMNORA_MCP_AI_TOKEN" \
    --format json >"$output" 2>"$CLI_STDERR"; then
    printf 'FAIL: MCP Inspector %s check failed\n' "$method" >&2
    exit 1
  fi
}

run_inspector "$INIT_JSON" initialize
run_inspector "$TOOLS_JSON" tools/list

grep -Eq '"protocolVersion"[[:space:]]*:[[:space:]]*"2026-07-28"' "$INIT_JSON" \
  || { printf 'FAIL: Inspector did not negotiate MCP 2026-07-28\n' >&2; exit 1; }

# Parse the Inspector JSON without jq so the gate remains portable. The
# catalogue may be scope-filtered; every returned name must still be one of
# the executable 24-tool contract, and at least one tool must be visible.
node - "$TOOLS_JSON" "${OMNORA_MCP_EXPECTED_TOOLS:-}" <<'NODE'
const fs = require("fs");
const file = process.argv[2];
const expectedRaw = process.argv[3] || "";
const known = new Set([
  "mounts.list", "files.list", "files.metadata", "files.search",
  "files.read_text", "files.prepare_download", "directories.create",
  "files.prepare_upload", "uploads.status", "uploads.complete", "uploads.cancel",
  "files.rename", "files.copy", "files.move", "files.trash", "trash.list",
  "trash.restore", "trash.purge", "trash.empty", "files.delete_permanently",
  "shares.list", "shares.create", "shares.revoke", "files.update",
]);
let payload;
try { payload = JSON.parse(fs.readFileSync(file, "utf8")); } catch (_) {
  console.error("FAIL: Inspector tools/list was not valid JSON"); process.exit(1);
}
const result = payload && payload.result ? payload.result : payload;
const tools = result && result.tools;
if (!Array.isArray(tools) || tools.length < 1 || tools.length > 24) {
  console.error("FAIL: Inspector returned an invalid scope-filtered tools/list catalogue"); process.exit(1);
}
for (const tool of tools) {
  if (!tool || typeof tool.name !== "string" || !known.has(tool.name)) {
    console.error("FAIL: Inspector returned a tool outside the 24-tool contract"); process.exit(1);
  }
}
const mountsList = tools.find(tool => tool.name === "mounts.list");
if (mountsList) {
  const schema = mountsList.inputSchema || {};
  const properties = schema.properties || {};
  const required = schema.required || [];
  if (Object.keys(properties).length !== 0 || required.length !== 0) {
    console.error("FAIL: mounts.list must have an empty input schema"); process.exit(1);
  }
}
const expected = expectedRaw.split(",").map(s => s.trim()).filter(Boolean);
for (const name of expected) {
  if (!tools.some(tool => tool.name === name)) {
    console.error("FAIL: expected scope-filtered tool is missing"); process.exit(1);
  }
}
NODE

printf 'MCP Inspector v2.1.0 initialize/tools-list checks passed\n'

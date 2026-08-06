# API and MCP Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish a maintainable REST/OpenAPI and MCP documentation system with one OpenAPI source, explicit current-versus-target MCP behavior, navigable human guides, and automated drift checks.

**Architecture:** `openapi/omnora.v1.yaml` remains the only hand-maintained REST contract. The Go `embed` copy under `internal/server/openapi_assets/` is generated from that source and checked for byte equality. Human-facing REST and MCP guides live separately under `docs/api/` and `docs/mcp/`; shared authorization and domain rules remain authoritative in the existing security and domain documents.

**Tech Stack:** OpenAPI 3.1 YAML, Markdown, POSIX shell verification scripts, Go HTTP handler behavior, existing `release-gate.sh` and `verify-scaffolding.sh` checks.

---

### Task 1: Establish the OpenAPI source and generated runtime asset

**Files:**
- Create: `scripts/verification/sync-openapi-asset.sh`
- Modify: `openapi/omnora.v1.yaml`
- Modify: `internal/server/openapi_assets/omnora.v1.yaml`
- Test: `scripts/verification/sync-openapi-asset.sh --check`

- [x] **Step 1: Align the source contract with the current HTTP response shapes**

  Update the `/mcp` operation to describe the current method-plus-params JSON adapter and its six current method names. Update the shared error response schema and media type to match `internal/httpx/errors.go`: `application/json` containing `{ "error": { "code": "...", "message": "...", "request_id": "..." } }`. Do not change the existing `protected` field in the `User` schema.

  Also align authentication with the current call path: REST resource handlers currently require the `omnora_session` cookie, while only `/mcp` currently consumes `Authorization: Bearer <AI_TOKEN>`. Make cookie session the OpenAPI default security requirement and keep bearer authentication scoped to `/mcp`; describe REST bearer-token support as a future contract until the handlers accept it.

- [x] **Step 2: Add a sync/check script with explicit source and target paths**

  The script must resolve the repository root from its own location, copy `openapi/omnora.v1.yaml` to `internal/server/openapi_assets/omnora.v1.yaml` for the default `sync` action, compare the two files for `--check`, print a unified diff on mismatch, reject unknown arguments, and exit non-zero when either file is missing.

- [x] **Step 3: Synchronize the embedded asset from the root contract**

  Run `scripts/verification/sync-openapi-asset.sh sync` and confirm `cmp -s openapi/omnora.v1.yaml internal/server/openapi_assets/omnora.v1.yaml` succeeds.

- [x] **Step 4: Verify the source contract still contains the current user change**

  Run `rg -n -A4 'protected:' openapi/omnora.v1.yaml` and confirm the `protected` field remains in the `User` schema after synchronization.

### Task 2: Add human-facing REST and MCP guides

**Files:**
- Create: `docs/api/README.md`
- Create: `docs/mcp/README.md`

- [x] **Step 1: Document REST usage without duplicating the OpenAPI schema**

  `docs/api/README.md` must link to `openapi/omnora.v1.yaml` and runtime `/openapi` endpoints, explain the `/api/v1` base path, cookie-session and bearer-token authentication, route-group enablement, common error envelope with `request_id`, cursor pagination, AI Token creation/revocation, upload/download boundaries, and `curl` examples for health, session-bound API access, and AI Token access. Field definitions must point to OpenAPI instead of being copied into prose.

- [x] **Step 2: Document the current MCP adapter exactly as implemented**

  `docs/mcp/README.md` must state that the current endpoint is `POST /mcp`, requires `Authorization: Bearer <AI_TOKEN>`, requires the MCP route group to be enabled, accepts `{ "method": "...", "params": { ... } }`, and currently exposes exactly `tools/list`, `spaces.list`, `files.search`, `files.list`, `files.metadata`, and `files.read_text`. It must list each method's required scope, parameters, output purpose, path-boundary behavior, the default/max text-read limit, and current unsupported operations such as upload, deletion, sharing, ACL management, and administration.

- [x] **Step 3: Separate current behavior from the target protocol statement**

  The MCP guide must explicitly say that the current handler is a small JSON request/response adapter and must not be presented as completed standards-compatible Streamable HTTP MCP. The existing design/security documents may retain target-state language, but the guide must link to them as design targets rather than claim implementation parity.

### Task 3: Update navigation and remove inaccurate top-level wording

**Files:**
- Modify: `README.md`
- Modify: `docs/README.md`

- [x] **Step 1: Add REST and MCP guides to the document maps**

  Add separate REST API and MCP guide rows to both document maps, with the OpenAPI YAML identified as the machine-readable REST contract.

- [x] **Step 2: Make the root product description match the current MCP implementation**

  Replace the top-level claim that the product already provides “Streamable HTTP MCP” with wording that identifies the current HTTP MCP entry point and links to the MCP guide for the implementation status.

- [x] **Step 3: Preserve the existing product and security source-of-truth hierarchy**

  Keep the current priority ordering in `docs/README.md`; add links and status notes without copying security or domain rules into the API/MCP guides.

### Task 4: Add documentation and contract verification to release checks

**Files:**
- Create: `scripts/verification/verify-api-docs.sh`
- Modify: `scripts/verification/verify-scaffolding.sh`
- Modify: `scripts/verification/release-gate.sh`

- [x] **Step 1: Add a focused API documentation verifier**

  The verifier must check that the root OpenAPI file, embedded copy, REST guide, MCP guide, and both navigation files exist; invoke `sync-openapi-asset.sh --check`; assert OpenAPI 3.1, `/mcp`, the documented MCP method names, the five current MCP read scopes, and the runtime error envelope fields are present; and fail on drift or missing documentation.

- [x] **Step 2: Wire the focused verifier into the scaffold and release gates**

  `verify-scaffolding.sh` must require the verifier to exist and be executable and run it during static checks. `release-gate.sh` must run the focused verifier as a separate step before the broader scaffold checks.

- [x] **Step 3: Run focused checks and inspect the final diff**

  Run:

  ```bash
  scripts/verification/sync-openapi-asset.sh --check
  scripts/verification/verify-api-docs.sh
  scripts/verification/verify-scaffolding.sh
  git diff --check
  git status --short
  ```

  Confirm that only the planned documentation, OpenAPI asset, and verification files changed in addition to the pre-existing user modifications.

---

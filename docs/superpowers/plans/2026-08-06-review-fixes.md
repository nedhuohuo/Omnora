# Omnora Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the deployment, OpenAPI, release-gate, and configurable-root defects found in the code review.

**Architecture:** Keep the entrypoint responsible for persistent-state bootstrap without exposing secrets; make OpenAPI the source of truth for runtime contracts; return typed host-directory entries so the UI never infers configured storage kinds from literal paths.

**Tech Stack:** POSIX shell, Go HTTP handlers/tests, OpenAPI YAML, GitHub Actions, React/TypeScript/Vitest.

---

### Task 1: Secure entrypoint bootstrap

**Files:**
- Modify: `deploy/docker-entrypoint.sh`
- Modify: `scripts/verification/test-docker-entrypoint.sh`

- [x] Stop printing the initialization token and retain only the protected runtime-env path.
- [x] Make marker creation recoverable after a pre-database process failure.
- [x] Add regression coverage for secret redaction and retry after a failed first process.

### Task 2: Synchronize API contract and publication gates

**Files:**
- Modify: `openapi/omnora.v1.yaml`
- Modify: `internal/server/openapi_assets/omnora.v1.yaml`
- Modify: `.github/workflows/publish-image.yml`

- [x] Pin `/mcp` to the runtime root server and document actual admin mount deletion and protected/error responses.
- [x] Keep the embedded OpenAPI asset byte-identical to the source.
- [x] Run the full release gate before image build/push.

### Task 3: Preserve configured host-directory kinds

**Files:**
- Modify: `internal/server/hostdirs.go`
- Modify: `internal/server/api.go`
- Modify: `web/src/api.ts`
- Modify: `web/src/member/AdminWorkspace.tsx`
- Test: `internal/server/hostdirs_test.go`, `web/src/member/*.test.*`

- [x] Return each configured root with its authoritative `managed`/`external` kind.
- [x] Drive cards, defaults, and selection from the typed response.
- [x] Cover custom configured roots in backend and frontend tests.

### Task 4: Verify

- [x] Run focused shell, Go, and Web tests.
- [x] Run Go tests excluding the sandbox-blocked listener test, Web tests/build, OpenAPI checks, and `git diff --check`.
- [x] Confirm the token is absent from entrypoint stderr and no browser diagnostic artifact remains tracked.

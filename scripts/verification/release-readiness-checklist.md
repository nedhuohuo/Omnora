# Omnora First-Version Verification Checklist

This checklist is local scaffolding for implementers. It summarizes release checks from the product, architecture, security, and acceptance documents without replacing those source documents.

## Deployment

- The default Compose deployment starts exactly one `omnora` application container.
- The image build compiles `web/dist` and embeds the production frontend in the Go binary.
- TOTP encryption and initialization secrets are generated and persisted by the container when not supplied; real values are never committed.
- The container does not require PostgreSQL, Redis, OpenSearch, MinIO, Office converters, FFmpeg, GPU, host networking, Docker socket access, or privileged mode.
- A single HTTP listener is exposed through Docker port publishing or a reverse proxy.
- Admin Web, Member Web, Share, REST, MCP, and OpenAPI exposure are independently configurable.
- Existing NAS directories are bind-mounted into predeclared container paths before registration.
- Reinstalling or recreating the container preserves access when the SQLite/config/managed volumes and original external bind paths are mounted back to the same container paths.

## Aliyun Test Server

- The Aliyun ECS test server is documented as a test-only candidate until ownership, workload, SSH, firewall, and security-group checks pass.
- The Aliyun test deployment intentionally binds HTTP `8080` to `0.0.0.0` for external browser access.
- `deploy/aliyun-test.env` and `deploy/aliyun-test/` runtime data are excluded from Git.
- Share route group is enabled in the Aliyun QA env example so public share-link flows can be exercised; MCP stays disabled by default.
- Real hostnames, root passwords, private keys, API tokens, initialization tokens, and TOTP encryption keys are not committed.
- `OMNORA_DEPLOY_ENV=aliyun-test` is treated only as metadata; Docker port binds, route groups, Aliyun security groups, host firewall, and reverse-proxy policy are the security boundary.

## API Contract

- The OpenAPI document is versioned under `/api/v1` and uses OpenAPI 3.1.
- Core REST groups cover identity, personal files, common mounts, directory collaborations, directory listing, metadata search, metadata read, bounded text preview, download tickets, resumable uploads, shares, AI Tokens, admin mount registration, audit, and MCP entry exposure.
- API errors include stable codes for route group denial, mount identity verification failure, read-only mounts, stale objects, quota, rate limits, and share-password requirements.
- Share secrets are represented as URL-fragment secrets exchanged by `POST`; path, query, and Referer submission are rejected by implementation tests.
- AI Token scopes are read-only by default, with upload session creation represented as an explicit additional scope. Token boundaries are limited to `all_account_content`, `personal`, and `common_mount`; received collaborations are always rejected by MCP.

## Local Smoke Checks

- Run `scripts/verification/release-gate.sh` for the local release-candidate gate.
- Run `scripts/verification/verify-scaffolding.sh` from any working directory.
- In default local mode, Docker Compose syntax is validated when Docker Compose is installed and otherwise covered by static checks.
- In strict release-candidate mode, Docker Compose syntax validation is mandatory.
- The smoke script does not call external services, pull images, or require a running Omnora server.

## Release Candidate Gates

- `scripts/verification/release-gate.sh` runs the Go suite, backup/restore test subset, frontend Vitest suite, frontend production build, and static Compose checks.
- `scripts/verification/verify-image-platforms.sh` verifies the Docker image builds for `linux/amd64` and `linux/arm64` with buildx.
- `scripts/verification/verify-deployed-http.sh <base-url>` verifies `/healthz`, `/readyz`, `X-Request-ID` correlation, and the browser entry from the caller's network.
- `scripts/verification/verify-nas-record.sh <record-file>` verifies a completed NAS deployment evidence record from `docs/deployment/nas-verification.md`.
- Use `scripts/verification/release-gate.sh --strict` for release candidates; strict mode fails when image, deployed HTTP, or NAS evidence inputs are missing.

## MCP Inspector and Protocol Gates

- `scripts/verification/verify-mcp-protocol.sh` runs the official Go SDK Streamable HTTP client tests, raw protocol negative tests, transfer coverage, and the catalogue/documentation contract without external network access.
- `internal/server` protocol tests negotiate modern `2026-07-28`, cover tested `2025-11-25` compatibility, verify the 24-tool/15-scope contract, and confirm form Elicitation MRTR retry for all 8 high-risk operations.
- The MCP catalogue contains `files.update` and no legacy Space tool. `mounts.list` accepts no input and only returns currently granted common mounts. File locators use only `personal` or `common_mount`.
- `scripts/verification/verify-mcp-inspector.sh` is an operator-only live check. Run it only with a short-lived scoped `OMNORA_MCP_AI_TOKEN`, `OMNORA_MCP_URL`, and a separate completed evidence file based on `docs/verification/mcp-inspector-checklist.md`.
- The Inspector check is conditional in `release-gate.sh`; when URL and token are absent the deterministic protocol gate still runs and the live check is skipped. When supplied, the gate rejects evidence with unchecked `- [ ]` items, requires all 22 checklist items from `docs/verification/mcp-inspector-checklist.md` to be checked, and requires `Status: COMPLETE` plus a redacted evidence summary. The token is never printed, but is visible to the local Inspector process while the command runs.
- Acceptance evidence is limited to MCP Inspector Modern / Streamable HTTP and protocol responses; no specific desktop or product client is required.

## Future Implementation Gates

- Web, REST, MCP, and share entry points call the same application services and authorization policy.
- File access revalidates current route group, subject, credential, ACL, scope, mount mode, object identity, quota, rate limit, and concurrency state.
- Mount registration rejects equal paths, parent-child paths, symlink or magic-link components, duplicate mount identity, bind aliases, and unverifiable identity.
- Search excludes disabled, paused, offline, unverifiable, and incomplete-index mounts with explicit reasons.
- Uploads and cross-mount copy/move use bounded memory and survive restart through durable task state; downloads use Range/streaming with bounded memory and do not require durable transfer-task state.
- Audit records cover identity, ACL, mount changes, file writes/deletes, share access, Token use, REST/MCP sensitive operations, and recovery without logging secrets or file content.

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
- Core REST groups cover identity, spaces, mounts, directory listing, metadata search, metadata read, bounded text preview, download tickets, resumable uploads, shares, AI Tokens, admin mount registration, audit, and MCP entry exposure.
- API errors include stable codes for route group denial, mount identity verification failure, read-only mounts, stale objects, quota, rate limits, and share-password requirements.
- Share secrets are represented as URL-fragment secrets exchanged by `POST`; path, query, and Referer submission are rejected by implementation tests.
- AI Token scopes are read-only by default, with upload session creation represented as an explicit additional scope.

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

## Future Implementation Gates

- Web, REST, MCP, and share entry points call the same application services and authorization policy.
- File access revalidates current route group, subject, credential, ACL, scope, mount mode, object identity, quota, rate limit, and concurrency state.
- Mount registration rejects equal paths, parent-child paths, symlink or magic-link components, duplicate mount identity, bind aliases, and unverifiable identity.
- Search excludes disabled, paused, offline, unverifiable, and incomplete-index mounts with explicit reasons.
- Uploads and cross-mount copy/move use bounded memory and survive restart through durable task state; downloads use Range/streaming with bounded memory and do not require durable transfer-task state.
- Audit records cover identity, ACL, mount changes, file writes/deletes, share access, Token use, REST/MCP sensitive operations, and recovery without logging secrets or file content.

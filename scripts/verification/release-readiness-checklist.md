# Omnora First-Version Verification Checklist

This checklist is local scaffolding for implementers. It summarizes release checks from the product, architecture, security, and acceptance documents without replacing those source documents.

## Deployment

- The default Compose deployment starts exactly one `omnora` application container.
- The image build compiles `web/dist` and embeds the production frontend in the Go binary.
- TOTP encryption and initialization secrets are required at container startup and are never committed.
- The container does not require PostgreSQL, Redis, OpenSearch, MinIO, Office converters, FFmpeg, GPU, host networking, Docker socket access, or privileged mode.
- `lan_http` and `proxy_https` are configured as separate entries with explicit binds and CIDR trust settings.
- Admin Web, Member Web, Share, REST, MCP, and OpenAPI exposure are independently configurable.
- Existing NAS directories are bind-mounted into predeclared container paths before registration.

## Aliyun Test Server

- The Aliyun ECS test server is documented as a test-only candidate until ownership, workload, SSH, firewall, and security-group checks pass.
- The Aliyun test Compose override binds Omnora to host-local addresses by default; testers use an SSH tunnel unless an explicit TLS reverse proxy is configured.
- `deploy/aliyun-test.env` and `deploy/aliyun-test/` runtime data are excluded from Git.
- Share and MCP route groups stay disabled by default on the test server.
- Real hostnames, root passwords, private keys, API tokens, initialization tokens, and TOTP encryption keys are not committed.
- `OMNORA_DEPLOY_ENV=aliyun-test` is treated only as metadata; network binds, CIDR trust, route groups, Aliyun security groups, host firewall, and reverse-proxy policy are the security boundary.

## API Contract

- The OpenAPI document is versioned under `/api/v1` and uses OpenAPI 3.1.
- Core REST groups cover identity, spaces, mounts, directory listing, metadata search, metadata read, bounded text preview, download tickets, resumable uploads, shares, AI Tokens, admin mount registration, audit, and MCP entry exposure.
- API errors include stable codes for route group denial, mount identity verification failure, read-only mounts, stale objects, quota, rate limits, and share-password requirements.
- Share secrets are represented as URL-fragment secrets exchanged by `POST`; path, query, and Referer submission are rejected by implementation tests.
- AI Token scopes are read-only by default, with upload session creation represented as an explicit additional scope.

## Local Smoke Checks

- Run `scripts/verification/verify-scaffolding.sh` from any working directory.
- If Docker Compose is installed, the script validates Compose syntax without starting containers.
- The smoke script does not call external services, pull images, or require a running Omnora server.

## Future Implementation Gates

- Web, REST, MCP, and share entry points call the same application services and authorization policy.
- File access revalidates current route group, subject, credential, ACL, scope, mount mode, object identity, quota, rate limit, and concurrency state.
- Mount registration rejects equal paths, parent-child paths, symlink or magic-link components, duplicate mount identity, bind aliases, and unverifiable identity.
- Search excludes disabled, paused, offline, unverifiable, and incomplete-index mounts with explicit reasons.
- Uploads, downloads, cross-mount copy, and cross-mount move use bounded memory and survive restart through durable task state.
- Audit records cover identity, ACL, emergency access, mount changes, file writes/deletes, share access, Token use, REST/MCP sensitive operations, and recovery without logging secrets or file content.

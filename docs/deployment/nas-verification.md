# NAS Deployment Verification Evidence

This document defines the evidence format for Omnora release-candidate NAS verification. It does not replace the product acceptance criteria; it gives operators a concrete record that can be checked by `scripts/verification/verify-nas-record.sh`.

## When To Use This

Create one evidence record for each release candidate after:

- The local release gate passes.
- The image builds for `linux/amd64` and `linux/arm64`.
- The candidate is deployed once on a ZSpace NAS-class host and once on a generic Linux Docker Compose host.
- External HTTP reachability and backup/restore rehearsal are complete.
- Application, container, and proxy log evidence can correlate at least one failed request by request ID.
- Reinstall or container recreation preserves access to files under the original registered mount paths.

Recommended record path:

```text
docs/deployment/evidence/YYYY-MM-DD-omnora-rc-nas-verification.md
```

The record may live outside Git if it contains hostnames, operator names, screenshots, or private infrastructure notes. If it is outside Git, pass its absolute path to `OMNORA_NAS_VERIFICATION_RECORD`.

## Machine-Readable Markers

A completed record must contain these exact lines:

```text
- zspace_nas: PASS
- generic_linux_compose: PASS
- compose_config: PASS
- external_reachability: PASS
- backup_restore_rehearsal: PASS
```

Use `PENDING` or omit the marker while work is incomplete. `verify-nas-record.sh` fails if placeholders, angle-bracket template values, pending markers, or required evidence fields remain empty.

## Evidence Template

Copy this template into a dated evidence record and replace every placeholder with concrete values.

````markdown
# Omnora RC NAS Verification Evidence

Release candidate: <tag-or-commit>
Verifier: <name>
Date: <YYYY-MM-DD>

## Result Markers

- zspace_nas: PENDING
- generic_linux_compose: PENDING
- compose_config: PENDING
- external_reachability: PENDING
- backup_restore_rehearsal: PENDING

## Local Release Gate

Command:

```bash
scripts/verification/release-gate.sh
```

Result:

```text
<paste final command summary>
```

## Multi-Architecture Image

Command:

```bash
OMNORA_IMAGE_TAG=<tag> scripts/verification/verify-image-platforms.sh
```

Evidence:

- Platforms verified: `linux/amd64`, `linux/arm64`
- Image tag or digest: `<tag-or-digest>`
- Build host: `<host>`

## ZSpace NAS-Class Host

Host summary:

- Device/model:
- CPU architecture:
- RAM:
- Docker/Compose version:
- Data path:
- External mount path:

Commands:

```bash
docker compose -f deploy/docker-compose.yml config --quiet
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml ps
```

Evidence:

- Member Web loaded:
- Admin Web loaded:
- Managed mount browse/upload/download:
- External read-only mount browse/download:
- External read-write mount create/delete with explicit confirmation:
- Reinstall preserves original managed/external files:
- Route-group configuration checked:
- Memory observation during normal browse/search/transfer:
- Application log format and level:
- Container log driver and rotation:
- Reverse-proxy request ID forwarding:
- Failed request correlation by request ID:

## Generic Linux Docker Compose Host

Host summary:

- OS:
- CPU architecture:
- RAM:
- Docker/Compose version:
- Data path:
- External mount path:

Commands:

```bash
docker compose -f deploy/docker-compose.yml config --quiet
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml ps
```

Evidence:

- Member Web loaded:
- Admin Web loaded:
- Managed mount browse/upload/download:
- External mount behavior:
- Reinstall preserves original managed/external files:
- Route-group configuration checked:
- Application log format and level:
- Container log driver and rotation:
- Reverse-proxy request ID forwarding:
- Failed request correlation by request ID:

## External Reachability

Command:

```bash
scripts/verification/verify-deployed-http.sh <base-url>
```

Evidence:

- Base URL:
- `/healthz`:
- `/readyz`:
- `/` browser entry:
- Optional OpenAPI check:

## Backup Restore Rehearsal

Scope:

- Backup created through Admin Web or REST:
- Restore requested:
- Integrity checked:
- Old sessions invalidated:
- Old Token/share/session tickets invalidated:
- External mounts require re-verification:

Evidence:

```text
<paste command output, audit event IDs, or operator notes>
```

## Notes

- Known release risks:
- Follow-up actions:
````

## Verification Command

After filling the evidence record and changing all markers to `PASS`, run:

```bash
scripts/verification/verify-nas-record.sh docs/deployment/evidence/YYYY-MM-DD-omnora-rc-nas-verification.md
```

For a strict release gate:

```bash
OMNORA_DEPLOYED_BASE_URL=<base-url> \
OMNORA_NAS_VERIFICATION_RECORD=<record-path> \
scripts/verification/release-gate.sh --strict
```

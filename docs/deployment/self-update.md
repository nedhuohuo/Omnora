# Web Self-Update

Omnora supports an administrator-driven application update from the admin console. The feature is available at **Admin -> Backups -> Updates** when the REST, admin Web, and member Web route groups are enabled.

The update endpoint accepts one `.tar.gz` release package. A package is an archive with exactly these regular files:

```text
manifest.json
omnora
omnora-recovery
```

`manifest.json` must use format version `1`, identify the release version and target `GOOS`/`GOARCH`, and contain the size and SHA-256 digest of both binaries. The server rejects unexpected entries, duplicate entries, symlinks, hard links, special files, malformed gzip/tar data, path traversal, size/hash mismatches, unsupported platforms, and packages larger than `OMNORA_UPDATE_MAX_PACKAGE_BYTES` (512 MiB by default).

## Runtime Flow

1. A full administrator session with recent reauthentication and a valid CSRF token uploads the package.
2. Omnora extracts it below `/var/lib/omnora/updates/releases`, validates the manifest and payload digests, and atomically writes the `pending` pointer.
3. The upload response is returned before the process requests a graceful shutdown.
4. The Docker entrypoint moves the pending release to `active` and preserves the old release as `previous`.
5. `restart: unless-stopped` starts the selected binary from the persistent update volume. The image layer at `/usr/local/bin/omnora` remains unchanged.
6. If the selected process exits with a non-signal failure, the entrypoint restores `previous` and starts that release. The failure is retained in the update status endpoint.
7. The **Rollback release** action writes a durable rollback marker. On the next startup the entrypoint restores `previous` and restarts the service.

Update state remains queryable at `GET /api/v1/admin/updates` after the original browser request has been disconnected by the restart.

## Release Package

Generate a package from the repository with:

```bash
scripts/release/create-update-package.sh 2026.08.09 ./omnora-update-2026.08.09.tar.gz
```

The script builds the production web bundle, embeds it into both Go binaries, and writes a platform-specific manifest. Cross-builds can set `GOOS` and `GOARCH`, for example:

```bash
GOOS=linux GOARCH=arm64 scripts/release/create-update-package.sh 2026.08.09 ./omnora-update-linux-arm64.tar.gz
```

The package must be built for the architecture of the running container. A package for another architecture is rejected before activation.

## Deployment Requirements

- `/var/lib/omnora` must be a writable persistent volume. The default Compose files set `OMNORA_UPDATE_DIR=/var/lib/omnora/updates`.
- The container must use `restart: unless-stopped` or an equivalent supervisor policy. Without a restart supervisor, the upload only stages the release and the service remains stopped after graceful shutdown.
- Recreating the container is supported when the `config`, `data`, `managed`, and external mount volumes are preserved. The active and rollback release pointers live in the data volume.
- This feature switches binaries inside the persistent data volume; it does not change a GHCR image tag or rebuild a Docker image. For immutable production image workflows, publish and deploy a new image instead.
- SHA-256 protects package transfer and storage integrity. The current implementation does not verify a vendor signature, so only expose the update action to trusted administrators and keep the admin route behind the deployment trust boundary. A signed release keyring should be added before accepting packages from less-trusted operators.

The update package never runs archive-provided install hooks and never replaces the SQLite database, runtime secrets, external mounts, supervisor scripts, or the read-only image filesystem.

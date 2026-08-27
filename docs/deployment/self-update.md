# Web Self-Update

Omnora supports administrator-driven application updates from **Admin -> Backups -> Updates**. The feature is fail-closed and is available only when the REST, admin Web, and member Web route groups are enabled and `OMNORA_UPDATE_SIGNING_PUBLIC_KEY_FILE` points to a readable Ed25519 public key outside the writable update volume.

## Trust Boundary

Generate a dedicated release-signing keypair on a protected release machine:

```bash
openssl genpkey -algorithm Ed25519 -out omnora-update-private.pem
openssl pkey -in omnora-update-private.pem -pubout -out omnora-update-public.pem
chmod 600 omnora-update-private.pem
```

Never place the private key on the Omnora host. Mount only `omnora-update-public.pem` into the container as a read-only file outside `/var/lib/omnora`, then set:

```dotenv
OMNORA_UPDATE_SIGNING_PUBLIC_KEY_FILE=/run/omnora-release/public.pem
```

For Compose, add a read-only bind mount such as:

```yaml
volumes:
  - ./release-keys/omnora-update-public.pem:/run/omnora-release/public.pem:ro
```

Leaving `OMNORA_UPDATE_SIGNING_PUBLIC_KEY_FILE` empty disables Web self-update. A missing, malformed, non-Ed25519, or unreadable configured key is a startup error.

## Package Protocol

The upload endpoint accepts one `.tar.gz` containing exactly these regular files:

```text
manifest.json
manifest.sig
omnora
omnora-recovery
```

`manifest.sig` is the raw 64-byte Ed25519 signature over the exact bytes of `manifest.json`. The manifest format version is `1` and declares:

- release version and target `GOOS`/`GOARCH`;
- exact database `schemaVersion` supported by the release;
- size and SHA-256 digest of both binaries.

The server rejects untrusted signatures, same-version or downgrade packages, mismatched Schema versions, unsupported platforms, malformed gzip/tar data, duplicate or unexpected entries, links, special files, path traversal, decompression-limit violations, and artifact size/hash mismatches. The compressed package limit is controlled by `OMNORA_UPDATE_MAX_PACKAGE_BYTES` and defaults to 512 MiB.

Web self-update deliberately requires the target Schema version to equal the live database Schema version. Releases containing database migrations must be deployed through the immutable image/offline migration process instead, because automatically restoring an older binary after an irreversible migration is unsafe.

## Runtime Flow

1. An administrator with recent reauthentication and a valid CSRF token uploads the signed package.
2. Omnora verifies the signature, platform, version, Schema version, archive structure, sizes, and hashes in a private staging directory.
3. The manager obtains an advisory cross-process update lease, writes a `preparing` pointer, and holds the lease through backup, authorization audit, and commit/cancel. The entrypoint never consumes this pointer; server startup can discard it only after acquiring the lease, proving no live preparation still owns it.
4. Omnora creates and validates a SQLite Online Backup under the managed backup directory, records it in the backup catalogue, and stores its backup ID in the release metadata.
5. The manager atomically commits `preparing` to `pending`. The HTTP response is returned before the process requests graceful shutdown.
6. The Docker entrypoint promotes `pending` to `active`, retains the old release as `previous`, and writes a durable health-validation marker.
7. The supervisor starts the candidate and polls `OMNORA_UPDATE_HEALTH_URL`. Readiness must report both `status: ready` and the expected embedded release version before `OMNORA_UPDATE_HEALTH_TIMEOUT_SECONDS` expires, then remain continuously ready through `OMNORA_UPDATE_STABILITY_SECONDS`.
8. A candidate that exits, hangs, reports an unexpected version, loses readiness during stabilization, or misses the deadline is terminated and replaced immediately by `previous` or the image's built-in binary. The failure remains visible in update status.
9. Once the stabilization window succeeds, later ordinary process failures are handled by the container restart policy and do not silently roll back a previously healthy release.
10. Manual rollback uses a durable request token. An audit failure removes only the matching token, preventing an unaudited rollback on a later restart.

The update state is stored below `/var/lib/omnora/updates`:

```text
releases/
incoming/
preparing
pending
active
previous
rollback
health-pending
failure
update.lock
```

## Creating a Release

The package script builds the production assets and binaries, embeds the release version, derives the current migration Schema version, writes the manifest, and signs it:

```bash
OMNORA_UPDATE_SIGNING_PRIVATE_KEY_FILE=./omnora-update-private.pem \
  scripts/release/create-update-package.sh 2026.08.09 ./omnora-update-2026.08.09.tar.gz
```

Cross-builds can set `GOOS` and `GOARCH`:

```bash
GOOS=linux GOARCH=arm64 \
OMNORA_UPDATE_SIGNING_PRIVATE_KEY_FILE=./omnora-update-private.pem \
  scripts/release/create-update-package.sh 2026.08.09 ./omnora-update-linux-arm64.tar.gz
```

The base image must also be built with a comparable release version, for example `--build-arg OMNORA_VERSION=2026.08.01`. If a signing key is configured while the running binary reports `dev` or another non-release identity, startup fails closed instead of disabling downgrade protection.

## Deployment Requirements

- `/var/lib/omnora` and the managed backup directory must be writable persistent volumes.
- The public signing key must be mounted read-only and must not live below the writable data, configuration, or update directories.
- The container must use `restart: unless-stopped` or an equivalent supervisor policy.
- `OMNORA_UPDATE_HEALTH_URL` must resolve from inside the container. The default is `http://127.0.0.1:8080/readyz`.
- `OMNORA_UPDATE_HEALTH_TIMEOUT_SECONDS` defaults to `60`; `OMNORA_UPDATE_HEALTH_INTERVAL_SECONDS` defaults to `2`; `OMNORA_UPDATE_STABILITY_SECONDS` defaults to `30`.
- Exactly one active Omnora container may own an update directory. The advisory lock serializes state mutation but does not coordinate rolling restarts across replicas.
- Recreating the container is supported when configuration, data, managed storage, and external mounts are preserved.
- This feature selects binaries from the persistent data volume. It does not change a GHCR image tag, rebuild an image, or alter the immutable image layer.

The package never runs archive-provided hooks and never replaces the SQLite database, runtime secrets, external mounts, entrypoint, or read-only image filesystem. The pre-update backup is retained as an operator recovery point; automatic binary rollback does not automatically restore the database because signed Web updates cannot change its Schema.

# Reinstall Data Continuity

Omnora does not store file contents in SQLite. Reinstalling the application or
recreating the container must preserve both:

- The Omnora state volume: database, config, secrets, managed storage.
- The original file directories mounted back to the same container paths.

If those paths are preserved, files under registered mounts remain readable and
usable after reinstall. If a host directory is replaced, moved to a different
container path, or mounted from a different filesystem identity, Omnora marks
the mount unavailable until an administrator re-verifies it.

## Required Persistent Paths

For the default Compose deployment, preserve these host-side directories:

| Container path | Default host path | Required after reinstall |
| --- | --- | --- |
| `/etc/omnora` | `deploy/config` | Yes |
| `/var/lib/omnora` | `deploy/data` | Yes |
| `/srv/omnora/managed` | `deploy/managed` | Yes |
| `/mnt/omnora/...` | operator-provided NAS bind paths | Yes, same container path |

Also preserve these secrets:

- `OMNORA_TOTP_ENCRYPTION_KEY`
- Any deployment-specific route-group env settings
- The operator vault entry for external mount source paths

Changing `OMNORA_INITIALIZATION_TOKEN` after initialization is safe; it is only a
one-time bootstrap token.

## Supported Reinstall Flow

1. Stop the old container without deleting the host data directories.
2. Install or pull the new Omnora image.
3. Start Compose with the same `deploy/config`, `deploy/data`, `deploy/managed`,
   and external NAS bind mounts.
4. Confirm `/readyz` is ready.
5. Browse an existing managed mount file.
6. Browse and download an existing external mount file.
7. If a mount is unavailable, inspect the admin mount list and use re-verify
   only after confirming the host directory is the intended original source.

Do not reinitialize Omnora with an empty database if you expect previous spaces,
mounts, ACLs, shares, Tokens, audit events, or upload sessions to remain.

## Incompatible Database Recovery

If Omnora detects that a versioned SQLite database is missing core tables or
columns required by its original schema, it does not attempt a partial data
migration. The container checkpoints and closes SQLite, renames the database in
place to:

```text
omnora.db.incompatible-<UTC timestamp>.bak
```

It then creates a fresh database from the current migrations and logs the backup
path and incompatibility reason. The operator must run first initialization
again. NAS file contents are not deleted or moved; only Omnora accounts, spaces,
mount registrations, grants, shares, and other database state start fresh. Keep
the `.bak` file until the replacement instance has been verified.

## Verification Command Sketch

Before reinstall, create a known file under a registered mount:

```bash
echo reinstall-check > /path/on/nas/reinstall-check.txt
```

After reinstall, use the same account to browse/download the file from the same
Omnora mount. The request should succeed without re-registering the mount.

When recording release evidence, include the container path, host path, file
name, request ID, and result.

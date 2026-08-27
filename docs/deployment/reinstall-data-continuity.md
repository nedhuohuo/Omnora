# Reinstall Data Continuity

Omnora does not store file contents in SQLite. Reinstalling the application or
recreating the container must preserve both:

- The Omnora state volume: database, config, secrets, managed storage.
- The original file directories mounted back to the same container paths.

If those paths are preserved, files under registered mounts remain readable and
usable after reinstall. If a host directory is replaced, moved to a different
container path, or mounted from a different filesystem identity, Omnora marks
the mount unavailable until an authorized governing administrator re-verifies it.

## Required Persistent Paths

For the default Compose deployment, preserve these host-side directories:

| Container path | Default host path | Required after reinstall |
| --- | --- | --- |
| `/etc/omnora` | `deploy/config` | Yes |
| `/var/lib/omnora` | `deploy/data` | Yes |
| `/srv/omnora/managed` | `deploy/managed` | Yes; reserved for the single `personal_default` mount, per-account personal directories, and personal trash |
| `/mnt/omnora/...` | operator-provided NAS bind paths | Yes, same container path |

Also preserve these secrets:

- `OMNORA_TOTP_ENCRYPTION_KEY`
- Any deployment-specific route-group env settings
- The operator vault entry for external mount source paths

The entrypoint writes the same non-secret instance marker to
`/etc/omnora/.omnora-instance-id`, `/var/lib/omnora/.omnora-instance-id`, and
`/srv/omnora/managed/.omnora-instance-id`. If config and data markers are
missing or disagree, startup stops instead of silently creating a new SQLite
instance. If the config and data markers match and the managed directory is
empty, startup creates the managed marker for that empty volume. A non-empty
managed directory without a matching marker is rejected; verify that the
original managed directory is mounted before repairing its marker. Do not
delete config or data markers to make the service start.

Changing `OMNORA_INITIALIZATION_TOKEN` after initialization is safe; it is only a
one-time bootstrap token.

## Supported Reinstall Flow

1. Stop the old container without deleting the host data directories.
2. Install or pull the new Omnora image.
3. Start Compose with the same `deploy/config`, `deploy/data`, `deploy/managed`,
   and external NAS bind mounts.
4. Confirm `/readyz` is ready.
5. Sign in with an existing account and browse an existing file in “My Files”.
6. Browse and download an existing external mount file.
7. If a mount is unavailable, inspect the authorized governance view and use
   re-verify only after confirming the host directory is the intended original
   source. Any administrator may handle a normal mount; restricted and default
   personal mounts require the initial administrator.

If startup reports `persistent state check failed`, inspect the effective
mounts before changing any application data:

```bash
docker inspect omnora --format '{{range .Mounts}}{{println .Source "->" .Destination}}{{end}}'
```

The original config and data host directories must be mounted to
`/etc/omnora` and `/var/lib/omnora` respectively.

Do not reinitialize Omnora with an empty database if you expect previous
accounts, personal-directory mappings, mounts, mount grants, collaborations,
shares, Tokens, audit events, or upload sessions to remain. Legacy control-plane
records are intentionally not restored; external mount data is never deleted.

## Verification Command Sketch

Before reinstall, create a known file in an account personal directory or an external common mount:

```bash
echo reinstall-check > /path/on/nas/reinstall-check.txt
```

After reinstall, use the same account to browse/download the file from “My Files”
or the same external common mount. The request should succeed without replacing
the personal-directory mapping or re-registering the external mount.

When recording release evidence, include the container path, host path, file
name, request ID, and result.

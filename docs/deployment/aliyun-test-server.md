# Aliyun Test Server

This document designates the Aliyun ECS machine recorded in the private operator vault as the Omnora test-server candidate. After ownership and workload checks pass, it may be used as test infrastructure only. It must not be treated as production infrastructure.

## Server Role

| Item | Value |
| --- | --- |
| Role | Omnora test server |
| Candidate host | `120.26.88.7` (Obsidian `阿里云SSH`) |
| SSH port | `22` (key-only; historical `2222` is obsolete) |
| Source note | Obsidian `服务器 IP 维护总表`, entry `阿里云SSH`（`120.26.88.7`；旧笔记误称 lisaSSH） |
| Status in operator notes | `active`; verify workload and security group before first use |
| Production warning | Not the `fundrisk.ggbangs.com` production origin |

Before the first deployment, confirm that the ECS instance is still owned by the operator account, that `22/tcp` is allowed in the Aliyun security group, and that no existing production workload depends on this host. The host currently runs RustDesk relay services, so use an isolated Compose project and do not reuse their ports or directories.

## Preflight Checklist

- Confirm the ECS instance is in the expected Aliyun account and region.
- Confirm the host is not serving production traffic, scheduled jobs, VPN/proxy exit traffic, or any personal data.
- Confirm SSH uses key-only authentication where possible; do not rely on root password login for normal operations.
- Confirm Aliyun security group allows inbound `8080/tcp` for the intended testers (current operator choice: public HTTP on `8080` for this disposable test box).
- Confirm host firewall and security group keep `8081` closed to the public internet.
- Confirm the test data directory is disposable and excluded from Git.
- Confirm any temporary reverse proxy has explicit Host, TLS, and source allowlist settings.

## Access Model

The current Aliyun test deployment intentionally exposes Member/Admin Web on the ECS public interface:

| Entry | Bind | Purpose |
| --- | --- | --- |
| HTTP `8080` | `0.0.0.0` | External browser access for testers |

External URL:

```text
http://120.26.88.7:8080
```

Matching env values on the host (`deploy/aliyun-test.env`):

```text
OMNORA_BIND=0.0.0.0
```

This is a disposable test-box choice, not a production security model. The checked-in QA env example sets `OMNORA_ROUTE_SHARE_ENABLED=true` so public share-link flows can be exercised; keep `OMNORA_ROUTE_MCP_ENABLED=false` unless a specific MCP test requires it. `OMNORA_DEPLOY_ENV=aliyun-test` is an environment label, not a security boundary. The actual boundary remains the Docker bind address, route-group switches, Aliyun security group, host firewall, and any reverse-proxy policy.

If Clash/Meta TUN is enabled locally and SSH to the host fails, add `120.26.88.7/32` to the `DIRECT` rules or temporarily disable that TUN route. The operator note records this as a required connectivity condition for SSH operations.

Optional SSH tunnel (only when you deliberately switch `OMNORA_BIND` back to `127.0.0.1`):

```bash
ssh aliyunssh -L 18080:127.0.0.1:8080
# then open http://127.0.0.1:18080
```

If remote HTTPS browser access is required later, put a TLS reverse proxy in front of `127.0.0.1:8080` and keep public route groups disabled unless a specific test requires them.

## Deployment Files

Use the base Compose file plus the Aliyun test override:

```bash
cp deploy/aliyun-test.env.example deploy/aliyun-test.env
mkdir -p deploy/aliyun-test/{config,data,managed,mounts}
sudo chown -R 1000:1000 deploy/aliyun-test
```

Edit `deploy/aliyun-test.env` on the server if you want to provide explicit values for:

```text
OMNORA_INITIALIZATION_TOKEN
OMNORA_TOTP_ENCRYPTION_KEY
```

Both values may be left blank on a new test deployment. The container entrypoint
generates them and persists them in `aliyun-test/config/runtime.env`; preserve
that file when recreating the container. It also writes matching instance
markers to `aliyun-test/config/.omnora-instance-id` and
`aliyun-test/data/.omnora-instance-id`; if those markers no longer match, the
container stops so a new empty instance cannot be created accidentally. When
restoring an existing data directory, keep using the original TOTP encryption
key.

Start the test server (this host uses Docker Compose 1.29.2, so prefer `docker-compose`):

```bash
cd deploy
docker-compose --env-file aliyun-test.env -f docker-compose.yml -f docker-compose.aliyun-test.yml up -d
```

Check status:

```bash
docker-compose --env-file aliyun-test.env -f docker-compose.yml -f docker-compose.aliyun-test.yml ps
docker logs --tail 80 omnora-aliyun-test
```

Troubleshoot by request ID:

```bash
curl -i -H 'X-Request-ID: aliyun-smoke-001' http://120.26.88.7:8080/readyz
docker logs --since 30m omnora-aliyun-test | grep aliyun-smoke-001
```

The test env example defaults to `OMNORA_LOG_FORMAT=json` and
`OMNORA_LOG_LEVEL=info`. Keep those defaults for external QA so application
logs, proxy access logs, and client error responses can be correlated by
`X-Request-ID`.

Stop the test server:

```bash
cd deploy
docker-compose --env-file aliyun-test.env -f docker-compose.yml -f docker-compose.aliyun-test.yml down
```

## Storage Mounts

| Container path | Host path | Purpose |
| --- | --- | --- |
| `/srv/omnora/managed` | `deploy/aliyun-test/managed` | Managed mounts (always read-write in the container) |
| `/mnt/omnora` | `deploy/aliyun-test/mounts` | Predeclared external mount root (read-write on this test box) |

Register external mounts under `/mnt/omnora/...`, or managed mounts under `/srv/omnora/managed/...`. Do not point mounts at `/srv/omnora/data` or other paths that are not bind-mounted into the container.

Effective write access is still `Docker volume mode ∩ Omnora mount mode`. If you later switch the Compose bind back to `:ro`, existing `read_write` mounts will fail create/upload until the volume is remounted read-write.

For reinstall or container recreation tests, keep `deploy/aliyun-test/config`,
`deploy/aliyun-test/data`, `deploy/aliyun-test/managed`, and
`deploy/aliyun-test/mounts` in place and mount them back to the same container
paths. Existing files under registered managed and external mounts must remain
browsable and downloadable without re-registering the mount. If a directory is
replaced or mounted to a different container path, Omnora should mark that mount
unavailable until an administrator confirms and re-verifies the intended source.

## Guardrails

- Keep `deploy/aliyun-test.env` untracked.
- Never store root passwords, private keys, API tokens, or generated TOTP encryption keys in this repository.
- Keep test data synthetic or explicitly disposable.
- Keep `OMNORA_ROUTE_MCP_ENABLED=false` by default. The QA env example enables `OMNORA_ROUTE_SHARE_ENABLED=true` only to cover public share-link tests; turn it off when share testing is not in scope.
- Treat open `0.0.0.0/0` CIDR on `8080` as test-only; do not copy this allowlist into production.
- Record any firewall, domain, or reverse-proxy changes in the operator vault after they are made.

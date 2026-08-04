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
- Confirm Aliyun security groups and host firewall do not expose Omnora `8080` or `8081` publicly.
- Confirm the test data directory is disposable and excluded from Git.
- Confirm any temporary reverse proxy has explicit Host, TLS, and source allowlist settings.

## Access Model

The default test configuration binds Omnora to `127.0.0.1` on the ECS host. Test access should go through an SSH tunnel:

```bash
ssh aliyunssh -L 18080:127.0.0.1:8080
```

If Clash/Meta TUN is enabled locally, add `120.26.88.7/32` to the `DIRECT` rules or temporarily disable that TUN route before opening the tunnel. The operator note records this as a required connectivity condition.

Then open:

```text
http://127.0.0.1:18080
```

Do not expose `8080` or `8081` directly to the public internet. `OMNORA_DEPLOY_ENV=aliyun-test` is an environment label, not a security boundary. The actual boundary is the bind address, CIDR checks, route-group switches, Aliyun security group, host firewall, and any reverse-proxy policy. If remote browser access is required, put a TLS reverse proxy in front of `127.0.0.1:8081`, keep `OMNORA_PROXY_HTTPS_ENABLED=true`, configure trusted proxy CIDRs explicitly, and keep public route groups disabled unless a specific test requires them.

## Deployment Files

Use the base Compose file plus the Aliyun test override:

```bash
cp deploy/aliyun-test.env.example deploy/aliyun-test.env
mkdir -p deploy/aliyun-test/{config,data,managed,mounts}
sudo chown -R 1000:1000 deploy/aliyun-test
```

Edit `deploy/aliyun-test.env` on the server and set fresh values for:

```text
OMNORA_INITIALIZATION_TOKEN
OMNORA_TOTP_ENCRYPTION_KEY
```

Start the test server:

```bash
cd deploy
docker compose --env-file aliyun-test.env -f docker-compose.yml -f docker-compose.aliyun-test.yml up -d
```

Check status:

```bash
docker compose --env-file aliyun-test.env -f docker-compose.yml -f docker-compose.aliyun-test.yml ps
docker logs --tail 80 omnora-aliyun-test
```

Stop the test server:

```bash
cd deploy
docker compose --env-file aliyun-test.env -f docker-compose.yml -f docker-compose.aliyun-test.yml down
```

## Guardrails

- Keep `deploy/aliyun-test.env` untracked.
- Never store root passwords, private keys, API tokens, or generated TOTP encryption keys in this repository.
- Keep test data synthetic or explicitly disposable.
- Keep `OMNORA_ROUTE_SHARE_ENABLED=false` and `OMNORA_ROUTE_MCP_ENABLED=false` by default.
- Record any firewall, domain, or reverse-proxy changes in the operator vault after they are made.

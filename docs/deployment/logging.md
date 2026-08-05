# Production Logging

Omnora writes application logs to stdout. Container, NAS, and reverse-proxy
layers should collect stdout/stderr without copying secrets into a separate log
file.

## Runtime Settings

Production deployments should use:

```bash
OMNORA_LOG_FORMAT=json
OMNORA_LOG_LEVEL=info
```

Development deployments may use `OMNORA_LOG_FORMAT=text` and
`OMNORA_LOG_LEVEL=debug`.

Supported values:

| Variable | Values | Default |
| --- | --- | --- |
| `OMNORA_LOG_FORMAT` | `json`, `text` | `text` outside Compose; Compose defaults to `json` |
| `OMNORA_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |

Every HTTP response includes `X-Request-ID`. If a reverse proxy sends a valid
`X-Request-ID`, Omnora preserves it; otherwise Omnora generates one. Error
responses also include `error.request_id`.

## Application Log Fields

Request completion logs include:

- `request_id`
- `method`
- `path`
- `status`
- `duration_ms`
- `bytes`
- `remote_addr`
- `user_agent`

Error response logs include:

- `request_id`
- `method`
- `path`
- `status`
- `error_code`

Do not log request bodies, `Authorization`, `Cookie`, share secrets, download
tickets, AI Tokens, passwords, TOTP secrets, file contents, or full query
strings containing user-supplied secrets.

## Docker Compose Collection

The default Compose file uses Docker's `local` log driver with bounded rotation:

```bash
OMNORA_LOG_MAX_SIZE=10m
OMNORA_LOG_MAX_FILE=5
```

Inspect recent logs:

```bash
docker compose -f deploy/docker-compose.yml logs --since 30m omnora
docker logs --tail 200 omnora
```

When reporting an incident, preserve:

- The failing user's visible `request_id` from the UI or API error.
- Application log lines matching that `request_id`.
- Reverse-proxy access log lines for the same request.
- Relevant audit event IDs, if a security or data mutation action was involved.

## Reverse Proxy Correlation

Configure the proxy to forward or create `X-Request-ID`, and to log the same
value in its access log. The access log should include method, path without
sensitive query string, response status, upstream status, request time, upstream
response time, host, and the request ID.

Example Nginx sketch:

```nginx
map $http_x_request_id $omnora_request_id {
    default $http_x_request_id;
    ""      $request_id;
}

proxy_set_header X-Request-ID $omnora_request_id;

log_format omnora '$remote_addr "$request_method $uri" '
                  'status=$status upstream=$upstream_status '
                  'request_time=$request_time upstream_time=$upstream_response_time '
                  'request_id=$omnora_request_id host=$host';
```

Avoid logging `$request` or `$args` when share secrets, download tickets, or
other sensitive values may appear in URLs.

## NAS Evidence

For each release candidate, the NAS evidence record must include:

- Application log format and level.
- Container log driver and rotation settings.
- Reverse-proxy request ID forwarding and access log evidence, when a proxy is
  used.
- One failed request where the same request ID appears in the client response,
  application log, and proxy log.

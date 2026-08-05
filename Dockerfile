FROM node:22-alpine AS web-build

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go-build

ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-build /src/web/dist/ /src/internal/server/static/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/omnora ./cmd/omnora

FROM alpine:3.22

RUN addgroup -S -g 1000 omnora \
    && adduser -S -D -H -u 1000 -G omnora omnora \
    && mkdir -p /etc/omnora /var/lib/omnora /srv/omnora/managed \
    && chown -R 1000:1000 /etc/omnora /var/lib/omnora /srv/omnora
COPY --from=go-build /out/omnora /usr/local/bin/omnora
COPY deploy/docker-entrypoint.sh /usr/local/bin/omnora-docker-entrypoint
RUN chmod 755 /usr/local/bin/omnora-docker-entrypoint

ENV TZ=Asia/Shanghai \
    UMASK=0027 \
    OMNORA_CONFIG_DIR=/etc/omnora \
    OMNORA_DATA_DIR=/var/lib/omnora \
    OMNORA_DB_PATH=/var/lib/omnora/omnora.db \
    OMNORA_HTTP_ADDR=0.0.0.0:8080 \
    OMNORA_LOG_FORMAT=json \
    OMNORA_LOG_LEVEL=info \
    OMNORA_INITIALIZATION_TOKEN_TTL=30m \
    OMNORA_MANAGED_STORAGE_DIR=/srv/omnora/managed \
    OMNORA_PREDECLARED_MOUNT_ROOT=/mnt/omnora \
    OMNORA_ROUTE_ADMIN_WEB_ENABLED=true \
    OMNORA_ROUTE_MEMBER_WEB_ENABLED=true \
    OMNORA_ROUTE_SHARE_ENABLED=false \
    OMNORA_ROUTE_REST_ENABLED=true \
    OMNORA_ROUTE_MCP_ENABLED=false \
    OMNORA_ROUTE_OPENAPI_ENABLED=true

USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/omnora-docker-entrypoint"]
CMD ["/usr/local/bin/omnora"]

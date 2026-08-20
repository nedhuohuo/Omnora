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
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/omnora-recovery ./cmd/omnora-recovery

FROM alpine:3.22

RUN apk add --no-cache su-exec \
    && addgroup -S -g 1000 omnora \
    && adduser -S -D -H -u 1000 -G omnora omnora \
    && mkdir -p /etc/omnora /var/lib/omnora /srv/omnora/managed \
    && chown -R 1000:1000 /etc/omnora /var/lib/omnora /srv/omnora
COPY --from=go-build /out/omnora /usr/local/bin/omnora
COPY --from=go-build /out/omnora-recovery /usr/local/bin/omnora-recovery
COPY deploy/docker-entrypoint.sh /usr/local/bin/omnora-docker-entrypoint
RUN chmod 755 /usr/local/bin/omnora-docker-entrypoint /usr/local/bin/omnora-recovery

ENV TZ=Asia/Shanghai \
    UMASK=0027 \
    OMNORA_CONFIG_DIR=/etc/omnora \
    OMNORA_DATA_DIR=/var/lib/omnora \
    OMNORA_DB_PATH=/var/lib/omnora/omnora.db \
    OMNORA_HTTP_ADDR=0.0.0.0:8080 \
    OMNORA_LOG_FORMAT=json \
    OMNORA_LOG_LEVEL=info \
    OMNORA_MANAGED_STORAGE_DIR=/srv/omnora/managed \
    OMNORA_PREDECLARED_MOUNT_ROOT=/mnt/omnora \
    OMNORA_UPDATE_DIR=/var/lib/omnora/updates \
    OMNORA_UPDATE_MAX_PACKAGE_BYTES=536870912 \
    OMNORA_MCP_ALLOWED_HOSTS="" \
    OMNORA_MCP_ALLOWED_ORIGINS="" \
    OMNORA_MCP_MAX_BODY_BYTES=1048576 \
    OMNORA_ROUTE_ADMIN_WEB_ENABLED=true \
    OMNORA_ROUTE_MEMBER_WEB_ENABLED=true \
    OMNORA_ROUTE_SHARE_ENABLED=false \
    OMNORA_ROUTE_REST_ENABLED=true \
    OMNORA_ROUTE_MCP_ENABLED=false \
    OMNORA_ROUTE_OPENAPI_ENABLED=true

USER root
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/omnora-docker-entrypoint"]
CMD ["/usr/local/bin/omnora"]

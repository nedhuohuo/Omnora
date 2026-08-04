FROM node:22-alpine AS web-build

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go-build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-build /src/web/dist/ /src/internal/server/static/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/omnora ./cmd/omnora

FROM alpine:3.22

RUN addgroup -S -g 1000 omnora \
    && adduser -S -D -H -u 1000 -G omnora omnora
COPY --from=go-build /out/omnora /usr/local/bin/omnora

USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/omnora"]

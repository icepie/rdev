# syntax=docker/dockerfile:1

FROM oven/bun:1.3.14-alpine AS web-build
WORKDIR /src
COPY package.json bun.lock tsconfig.json vite.config.ts ./
COPY web ./web
COPY internal/server/static ./internal/server/static
RUN bun install --frozen-lockfile && bun run build

FROM golang:1.25-alpine AS go-build
WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
COPY go.mod go.sum ./
COPY internal/sshlib/go.mod ./internal/sshlib/go.mod
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web-build /src/internal/server/static ./internal/server/static
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/rdev-server ./cmd/rdev-server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && mkdir -p /data
COPY --from=go-build /out/rdev-server /usr/local/bin/rdev-server
VOLUME ["/data"]
EXPOSE 8080 2222 15900
ENTRYPOINT ["/usr/local/bin/rdev-server"]
CMD ["--http", ":8080", "--ssh", ":2222", "--data", "/data", "--no-auto-update"]

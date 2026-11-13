# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.22

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /routald ./cmd/routald

# Container rootfs of the extension service: nft and its libraries, plus routald.
FROM alpine:${ALPINE_VERSION} AS rootfs
RUN apk add --no-cache nftables && rm -rf /var/cache/apk /etc/apk /lib/apk /sbin/apk
COPY --from=build /routald /usr/local/bin/routald

# Talos system extension layout.
FROM scratch AS extension
COPY extension/routald.yaml /rootfs/usr/local/etc/containers/routald.yaml
COPY --from=rootfs / /rootfs/usr/local/lib/containers/routald/

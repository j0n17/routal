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

# Alpine's miniupnpd only has the iptables backend; Talos uses nftables.
FROM alpine:${ALPINE_VERSION} AS miniupnpd
ARG MINIUPNPD_VERSION=2_3_9
ARG MINIUPNPD_SHA256=ec7981351ad6a046eee0abf522ed1a45a3b0517e1da64e03826051f5f5354ea5
RUN apk add --no-cache build-base linux-headers bsd-compat-headers libnftnl-dev libmnl-dev openssl-dev util-linux-dev curl
WORKDIR /build
RUN curl -fsSL -o src.tar.gz https://github.com/miniupnp/miniupnp/archive/refs/tags/miniupnpd_${MINIUPNPD_VERSION}.tar.gz \
    && echo "${MINIUPNPD_SHA256}  src.tar.gz" | sha256sum -c - \
    && tar xzf src.tar.gz --strip-components=1
WORKDIR /build/miniupnpd
RUN ./configure --firewall=nftables --ipv6 --igd2 --leasefile --portinuse \
    && make -j"$(nproc)" miniupnpd && install -D -m 755 miniupnpd /out/usr/sbin/miniupnpd

# The daemons routald supervises.
FROM alpine:${ALPINE_VERSION} AS daemons
RUN apk add --no-cache nftables iproute2-minimal kea-dhcp4 kea-dhcp6 unbound radvd chrony avahi libnftnl libmnl libuuid \
    # mount points: persistent state, and a tmpfs for pid files and sockets (the rootfs is read-only)
    && mkdir -p /var/lib/routal /run
COPY --from=miniupnpd /out/ /
COPY --from=build /routald /usr/local/bin/routald

# Container rootfs of the extension service.
FROM daemons AS rootfs
RUN rm -rf /var/cache/apk /etc/apk /lib/apk /sbin/apk

# Talos system extension layout.
FROM scratch AS extension
COPY extension/routald.yaml /rootfs/usr/local/etc/containers/routald.yaml
COPY --from=rootfs / /rootfs/usr/local/lib/containers/routald/

# Integration tests (make integration): the same daemons plus clients, in network namespaces.
FROM daemons AS integration
RUN apk add --no-cache iproute2 bind-tools curl socat wireguard-tools miniupnpc dhcpcd tcpdump busybox-extras
COPY test/integration /test
COPY examples/nas-vpn/router.yaml /test/nas-vpn.yaml
ENTRYPOINT ["/test/run.sh"]

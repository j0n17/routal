# Build, install, upgrade

Requires Docker (Go builds and Talos tools run in containers).

## Build

```sh
make extension ARCH=arm64      # _out/routald-arm64.tar (flat extension for the imager)
make image ARCH=arm64          # _out/metal-arm64.raw, Talos with routald baked in
make installer ARCH=arm64      # _out/installer-arm64.tar, the Talos installer with routald (OCI image)
make push-installer IMAGE=ghcr.io/<you>/routal-installer:v0.1.0
```

## Install

Boot the image, apply `talosctl gen config --skip-k8s-etcd` plus your `talos.yaml`, with
`UnattendedInstallConfig` pointing at your installer (see the reference).

> [!IMPORTANT]
> The stock installer would drop the extension: always point at the Routal installer.

## Upgrade

```sh
talosctl upgrade --image <installer>:<new tag>
talosctl reboot     # in Kubernetes-free mode Talos does not reboot by itself
```

Use a new tag per build: Talos reuses an image it already pulled under the same tag.

## Releases

A tag `vX.Y.Z` publishes (`.github/workflows/release.yml`):

- the installer with the extension for every tested Talos version, amd64 and arm64, as
  `ghcr.io/<owner>/routal-installer:<X.Y.Z>-talos-<talos version>`;
- the extension tarballs on a GitHub release.

Supported Talos: 1.14 and later.

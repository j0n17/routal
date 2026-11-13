#!/bin/sh
# Turn an extension image into the flat tarball the Talos imager expects (manifest.yaml + rootfs/).
# usage: flatten-extension.sh <image> <platform> <out.tar> <manifest.yaml>
set -eu

image=$1 platform=$2 out=$3 manifest=$4
dir=$(cd "$(dirname "$out")" && pwd)

cid=$(docker create --platform "$platform" "$image" /none)
trap 'docker rm "$cid" >/dev/null' EXIT

docker export "$cid" -o "$dir/.export.tar"
cp "$manifest" "$dir/.manifest.yaml"
docker run --rm -v "$dir":/w -w /tmp alpine:3.22 sh -c \
  'tar xf /w/.export.tar rootfs && cp /w/.manifest.yaml manifest.yaml && tar cf "/w/'"$(basename "$out")"'" manifest.yaml rootfs'
rm -f "$dir/.export.tar" "$dir/.manifest.yaml"
echo "extension: $out"

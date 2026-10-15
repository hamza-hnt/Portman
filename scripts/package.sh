#!/bin/sh
set -eu
task_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$task_root"
task_version=$(cat VERSION)
task_os=$(go env GOOS)
task_arch=$(go env GOARCH)
case "$task_os/$task_arch" in
  darwin/arm64|darwin/amd64) task_cgo=1 ;;
  linux/arm64|linux/amd64) task_cgo=0 ;;
  *) echo "Unsupported package target: $task_os/$task_arch" >&2; exit 1 ;;
esac
task_temp=$(mktemp -d)
trap 'rm -rf -- "$task_temp"' EXIT
trap 'exit 1' HUP INT TERM
CGO_ENABLED=$task_cgo go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$task_version" -o "$task_temp/portman" ./cmd/portman
cp LICENSE README.md VERSION install.sh "$task_temp/"
mkdir -p "$task_temp/assets/brand"
cp assets/brand/portman-logo.png "$task_temp/assets/brand/"
mkdir -p dist
task_asset="portman_${task_os}_${task_arch}.tar.gz"
COPYFILE_DISABLE=1 tar --format=ustar -czf "dist/$task_asset" -C "$task_temp" portman LICENSE README.md VERSION install.sh assets
if command -v sha256sum >/dev/null 2>&1; then
  (cd dist && sha256sum "$task_asset" > "$task_asset.sha256")
else
  (cd dist && shasum -a 256 "$task_asset" > "$task_asset.sha256")
fi
printf 'Packaged dist/%s\n' "$task_asset"

#!/bin/sh
# Install a verified release binary, a local release archive, or this checkout.
set -eu

usage() {
  cat <<'USAGE'
Portman installer — macOS / Linux, arm64 / amd64

  ./install.sh --repo OWNER/REPO [--version v0.2.0]
  ./install.sh --archive /path/to/portman_OS_ARCH.tar.gz
  ./install.sh --source

Options:
  --prefix DIR       Install to DIR/bin (default: $HOME/.local)
  --repo OWNER/REPO   GitHub repository containing Portman release assets
  --version TAG      Release tag (default: latest; used with --repo)
  --archive FILE     Local release archive; requires FILE.sha256 beside it
  --source           Build this checkout (requires Go; macOS also needs Xcode CLI tools)
  --help             Show this help

With no mode specified, a source checkout is built locally. Release installs
do not need Go, a compiler, Homebrew or sudo. Shell profiles are not modified.
USAGE
}
fail() { printf 'portman installer: %s\n' "$*" >&2; exit 1; }
need_value() { [ "$#" -ge 2 ] && [ -n "$2" ] || fail "$1 needs a value"; }

task_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
task_prefix=${PREFIX:-"$HOME/.local"}
task_repo=""
task_tag="latest"
task_archive=""
task_mode=""
set_mode() {
  [ -z "$task_mode" ] || fail "choose only one of --repo, --archive or --source"
  task_mode=$1
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --prefix) need_value "$@"; task_prefix=$2; shift 2 ;;
    --repo) need_value "$@"; set_mode release; task_repo=$2; shift 2 ;;
    --version) need_value "$@"; task_tag=$2; shift 2 ;;
    --archive) need_value "$@"; set_mode archive; task_archive=$2; shift 2 ;;
    --source) set_mode source; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1 (use --help)" ;;
  esac
done

case "$(uname -s)" in
  Darwin) task_os=darwin; task_cgo=1 ;;
  Linux) task_os=linux; task_cgo=0 ;;
  *) fail "supported systems: macOS and Linux" ;;
esac
case "$(uname -m)" in
  arm64|aarch64) task_arch=arm64 ;;
  x86_64|amd64) task_arch=amd64 ;;
  *) fail "supported architectures: arm64 and amd64" ;;
esac
[ -n "$task_prefix" ] || fail "empty install prefix"
if [ -z "$task_mode" ]; then
  [ -f "$task_root/go.mod" ] || fail "use --repo OWNER/REPO or --archive FILE (see --help)"
  task_mode=source
fi
if [ "$task_mode" != release ] && [ "$task_tag" != latest ]; then
  fail "--version is only supported with --repo"
fi
task_temp=$(mktemp -d "${TMPDIR:-/tmp}/portman-install.XXXXXX")
task_staged=""
cleanup() {
  [ -z "$task_staged" ] || rm -f -- "$task_staged"
  rm -rf -- "$task_temp"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

if [ "$task_mode" = source ]; then
  [ -f "$task_root/go.mod" ] && [ -f "$task_root/VERSION" ] || fail "--source requires a Portman checkout"
  command -v go >/dev/null 2>&1 || fail "source builds require Go 1.24+; use a release archive to install without Go"
  if [ "$task_os" = darwin ]; then
    xcrun --find clang >/dev/null 2>&1 || fail "install Xcode Command Line Tools: xcode-select --install"
  fi
  task_version=$(cat "$task_root/VERSION")
  printf 'Building Portman %s for %s/%s…\n' "$task_version" "$task_os" "$task_arch"
  (cd "$task_root" && GOOS=$task_os GOARCH=$task_arch CGO_ENABLED=$task_cgo go build -buildvcs=false -trimpath -ldflags "-s -w -X main.version=$task_version" -o "$task_temp/portman" ./cmd/portman)
else
  task_asset="portman_${task_os}_${task_arch}.tar.gz"
  if [ "$task_mode" = release ]; then
    printf '%s\n' "$task_repo" | LC_ALL=C grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || fail "--repo must be OWNER/REPO"
    printf '%s\n' "$task_tag" | LC_ALL=C grep -Eq '^(latest|v?[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?)$' || fail "invalid version tag"
    command -v curl >/dev/null 2>&1 || fail "curl is required to download releases"
    if [ "$task_tag" = latest ]; then
      task_url="https://github.com/$task_repo/releases/latest/download"
    else
      task_url="https://github.com/$task_repo/releases/download/$task_tag"
    fi
    task_archive="$task_temp/$task_asset"
    printf 'Downloading %s (%s)…\n' "$task_asset" "$task_tag"
    curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 "$task_url/$task_asset" -o "$task_archive" || fail "download failed; check the repository and published release"
    curl --fail --location --silent --show-error --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 60 "$task_url/$task_asset.sha256" -o "$task_archive.sha256" || fail "release checksum is missing"
  fi
  [ -f "$task_archive" ] && [ -f "$task_archive.sha256" ] || fail "archive and matching .sha256 file are required"
  [ "$(basename -- "$task_archive")" = "$task_asset" ] || fail "expected $task_asset for this machine"
  task_expected=$(awk -v asset="$task_asset" 'NF == 2 && $2 == asset {print $1}' "$task_archive.sha256")
  [ "${#task_expected}" -eq 64 ] || fail "invalid SHA-256 file"
  case "$task_expected" in *[!0-9a-fA-F]*) fail "invalid SHA-256 digest" ;; esac
  if command -v sha256sum >/dev/null 2>&1; then
    task_actual=$(sha256sum "$task_archive" | cut -d ' ' -f 1)
  elif command -v shasum >/dev/null 2>&1; then
    task_actual=$(shasum -a 256 "$task_archive" | cut -d ' ' -f 1)
  else
    fail "sha256sum or shasum is required"
  fi
  [ "$task_actual" = "$task_expected" ] || fail "SHA-256 mismatch; nothing was installed"
  # Stream just the executable into our own file; never unpack archive paths.
  tar -xOf "$task_archive" portman > "$task_temp/portman" || fail "archive does not contain portman"
  [ -s "$task_temp/portman" ] || fail "empty executable in archive"
fi

chmod 755 "$task_temp/portman"
"$task_temp/portman" --version || fail "binary is incompatible with this machine"
mkdir -p -- "$task_prefix/bin"
task_bin=$(CDPATH= cd -- "$task_prefix/bin" && pwd)
[ ! -d "$task_bin/portman" ] || fail "destination is a directory: $task_bin/portman"
# Replace by rename on the same filesystem, including while Portman is running.
task_staged=$(mktemp "$task_bin/.portman.XXXXXX")
cat "$task_temp/portman" > "$task_staged"
chmod 755 "$task_staged"
mv -f -- "$task_staged" "$task_bin/portman"
task_staged=""
printf 'Installed: %s/portman\n' "$task_bin"
case ":$PATH:" in
  *":$task_bin:"*) printf 'Run: portman\n' ;;
  *) printf 'Add %s to your PATH, then run portman.\n' "$task_bin" ;;
esac

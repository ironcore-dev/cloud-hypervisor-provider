#!/usr/bin/env bash
set -euo pipefail

VERSION="${VERSION:-50.0}"
ARCH="${ARCH:-x86_64}"        # x86_64 or aarch64
BASE_DIR="/usr/local/cloud-hypervisor"


normalize_arch() {
  case "$1" in
    amd64|x86_64)  echo "x86_64" ;;
    arm64|aarch64) echo "aarch64" ;;
    *)
      echo "ERROR: Unsupported architecture '$1'" >&2
      exit 1
      ;;
  esac
}

ARCH="$(normalize_arch "$ARCH")"
HOST_ARCH="$(normalize_arch "$(uname -m)")"

echo "Install settings:"
echo "  VERSION   = ${VERSION}"
echo "  ARCH      = ${ARCH}"
echo "  HOST_ARCH = ${HOST_ARCH}"

if [[ "$ARCH" != "$HOST_ARCH" ]]; then
  echo "ERROR: ARCH=${ARCH} does not match host architecture (${HOST_ARCH})." >&2
  echo "Refusing to install wrong binary (would cause 'Exec format error')." >&2
  exit 2
fi

case "$ARCH" in
  x86_64)
    CHP_ASSET="cloud-hypervisor-static"
    CHR_ASSET="ch-remote"
    ;;
  aarch64)
    CHP_ASSET="cloud-hypervisor-static-aarch64"
    CHR_ASSET="ch-remote-static-aarch64"
    ;;
esac

BASE_URL="https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v${VERSION}"
DEST_DIR="${BASE_DIR}/${VERSION}/bin"

install_bin() {
  local name="$1"
  local asset="$2"

  local url="${BASE_URL}/${asset}"
  local dest="${DEST_DIR}/${name}"

  echo "Installing ${name}"
  echo "  url:  ${url}"
  echo "  dest: ${dest}"

  local tmp
  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' RETURN

  curl -fL "$url" -o "$tmp"
  chmod 0755 "$tmp"
  sudo mkdir -p "$DEST_DIR"
  sudo mv "$tmp" "$dest"
}

install_bin "cloud-hypervisor" "$CHP_ASSET"
install_bin "ch-remote" "$CHR_ASSET"

# Optional sanity checks
"${DEST_DIR}/cloud-hypervisor" --version || true
"${DEST_DIR}/ch-remote" --help >/dev/null 2>&1 || true


echo "All done."

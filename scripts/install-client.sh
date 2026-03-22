#!/usr/bin/env sh
set -eu

INSTALL_DIR="${INSTALL_DIR:-$HOME/.ssh-arena/bin}"
REPO_TGZ="https://github.com/Cricko7/ssh-arena/archive/refs/heads/main.tar.gz"
TMP_ROOT="$(mktemp -d)"
SRC_ROOT="$TMP_ROOT/src"
REPO_ROOT="$SRC_ROOT/ssh-arena-main"
BINARY_PATH="$INSTALL_DIR/game-client"

cleanup() {
  rm -rf "$TMP_ROOT"
}
trap cleanup EXIT

if ! command -v go >/dev/null 2>&1; then
  echo "Go toolchain is required to build the ssh-arena client. Install Go 1.24+ first." >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR" "$SRC_ROOT"
curl -fsSL "$REPO_TGZ" | tar -xzf - -C "$SRC_ROOT"
cd "$REPO_ROOT"
GOCACHE="$TMP_ROOT/gocache" go build -o "$BINARY_PATH" ./cmd/game-client

printf 'Installed ssh-arena client to %s\n' "$BINARY_PATH"
if [ "$#" -gt 0 ]; then
  printf 'Launching: %s %s\n' "$BINARY_PATH" "$*"
  exec "$BINARY_PATH" "$@"
fi
printf 'Run it with: %s\n' "$BINARY_PATH"

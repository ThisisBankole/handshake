#!/bin/sh
# Handshake installer
# Usage: curl -fsSL https://raw.githubusercontent.com/ThisisBankole/handshake/main/install.sh | sh

set -e

REPO="ThisisBankole/handshake"
BINARY="handshake"

# ── Detect OS and architecture ─────────────────────────────────────────────

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "✗ Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

case "$OS" in
  darwin|linux) ;;
  *)
    echo "✗ Unsupported OS: $OS"
    echo "  Install via Homebrew on Mac: brew install ThisisBankole/tools/handshake"
    exit 1
    ;;
esac

# ── Fetch latest release version ───────────────────────────────────────────

VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
  | grep '"tag_name"' \
  | sed 's/.*"tag_name": *"\(.*\)".*/\1/')

if [ -z "$VERSION" ]; then
  echo "✗ Failed to fetch latest version from GitHub"
  exit 1
fi

# ── Build download URL ─────────────────────────────────────────────────────
# GoReleaser produces archives named: handshake_darwin_arm64.tar.gz

ARCHIVE="${BINARY}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$VERSION/$ARCHIVE"

echo "Installing Handshake $VERSION ($OS/$ARCH)..."

# ── Download and extract ───────────────────────────────────────────────────

TMP=$(mktemp -d)

if ! curl -fsSL "$URL" -o "$TMP/$ARCHIVE"; then
  echo "✗ Failed to download $URL"
  rm -rf "$TMP"
  exit 1
fi

tar -xzf "$TMP/$ARCHIVE" -C "$TMP"
rm "$TMP/$ARCHIVE"

# ── Install binary ─────────────────────────────────────────────────────────
# Try /usr/local/bin first; fall back to ~/.local/bin if not writable.

INSTALL_DIR="/usr/local/bin"
if [ ! -w "$INSTALL_DIR" ]; then
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
fi

mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
chmod +x "$INSTALL_DIR/$BINARY"
rm -rf "$TMP"

echo "✓ Handshake $VERSION installed to $INSTALL_DIR/$BINARY"
echo ""

# ── Setup ──────────────────────────────────────────────────────────────────
# curl pipes the installer into sh, so setup reads and writes through /dev/tty
# when a terminal is available. CI and other non-interactive environments keep
# the previous unattended registration path.

# ── Hand the running daemon over to the new binary ─────────────────────────
# An already-running daemon keeps executing the old code it loaded at start.
# When a managed service exists, restart it explicitly so upgrades take effect
# immediately; otherwise just clear stale processes that may hold the port.

RESTARTED=""
if [ "$OS" = "darwin" ] && [ -f "$HOME/Library/LaunchAgents/com.handshake.serve.plist" ]; then
  if launchctl kickstart -k "gui/$(id -u)/com.handshake.serve" 2>/dev/null; then
    RESTARTED="yes"
  fi
elif [ "$OS" = "linux" ] && command -v systemctl >/dev/null 2>&1 \
  && systemctl --user cat handshake.service >/dev/null 2>&1; then
  if systemctl --user restart handshake.service 2>/dev/null; then
    RESTARTED="yes"
  fi
fi

if [ -n "$RESTARTED" ]; then
  sleep 1 # give the daemon a moment to bind before setup probes the port
  echo "✓ Restarted the Handshake service on $VERSION"
else
  # No managed service (or restart failed): kill any stale handshake serve
  # processes that might be holding the port. Setup reinstalls the service.
  pkill -f "handshake serve" 2>/dev/null || true
fi

if [ -t 1 ] && [ -r /dev/tty ] && [ -w /dev/tty ]; then
  echo "Starting guided setup..."
  "$INSTALL_DIR/$BINARY" setup </dev/tty >/dev/tty 2>&1
else
  echo "Running non-interactive setup..."
  "$INSTALL_DIR/$BINARY" init
  "$INSTALL_DIR/$BINARY" install-service
  echo "Project knowledge is not configured. Run: handshake setup"
fi
echo ""

# ── PATH check ─────────────────────────────────────────────────────────────

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo "  $INSTALL_DIR is not in your PATH. Add it:"
    echo ""
    if [ -n "$ZSH_VERSION" ] || [ "$(basename "$SHELL")" = "zsh" ]; then
      echo "    echo 'export PATH=\"\$PATH:$INSTALL_DIR\"' >> ~/.zshrc"
      echo "    source ~/.zshrc"
    else
      echo "    echo 'export PATH=\"\$PATH:$INSTALL_DIR\"' >> ~/.bashrc"
      echo "    source ~/.bashrc"
    fi
    echo ""
    ;;
esac

# ── Done ───────────────────────────────────────────────────────────────────

echo "✓ Handshake is ready."
echo "Session checkpoints stay local. Optional project knowledge uses the writer you choose."

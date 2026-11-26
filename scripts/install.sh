#!/bin/sh
# Install the latest coop release into ~/.local/bin (or $COOP_INSTALL_DIR).
#   curl -fsSL https://raw.githubusercontent.com/xpresstoparadise/coop-cli/main/scripts/install.sh | sh
set -eu

REPO=xpresstoparadise/coop-cli
DIR=${COOP_INSTALL_DIR:-$HOME/.local/bin}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
case $os in linux|darwin) ;; *) echo "unsupported OS: $os" >&2; exit 1 ;; esac

tag=${COOP_VERSION:-$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##')}
name=coop_${tag#v}_${os}_${arch}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading coop $tag for $os/$arch"
curl -fsSL -o "$tmp/$name.tar.gz" "https://github.com/$REPO/releases/download/$tag/$name.tar.gz"
curl -fsSL -o "$tmp/checksums.txt" "https://github.com/$REPO/releases/download/$tag/checksums.txt"
(cd "$tmp" && grep " $name.tar.gz\$" checksums.txt | { sha256sum -c - 2>/dev/null || shasum -a 256 -c -; } >/dev/null) \
  || { echo "checksum mismatch" >&2; exit 1; }
tar -C "$tmp" -xzf "$tmp/$name.tar.gz"
mkdir -p "$DIR"
install -m 0755 "$tmp/$name/coop" "$DIR/coop"
echo "installed $DIR/coop ($("$DIR/coop" version))"
case ":$PATH:" in *":$DIR:"*) ;; *) echo "add $DIR to your PATH" ;; esac

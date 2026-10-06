#!/bin/sh
# Installs the latest rig release (or RIG_VERSION) for this machine:
#   curl -fsSL https://raw.githubusercontent.com/goxang/rig/main/install.sh | sh
# RIG_INSTALL_DIR picks the directory (default /usr/local/bin when writable, else ~/.local/bin);
# RIG_VERSION=v0.3.0 a release.
set -eu

repo=goxang/rig
version=${RIG_VERSION:-latest}

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "rig: no release for $(uname -s); build it with: go install github.com/$repo/cmd/rig@latest" >&2; exit 1 ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "rig: no release for $(uname -m); build it with: go install github.com/$repo/cmd/rig@latest" >&2; exit 1 ;;
esac

# RIG_DOWNLOAD_URL points at a mirror holding the release assets
if [ -n "${RIG_DOWNLOAD_URL:-}" ]; then
  base=$RIG_DOWNLOAD_URL
elif [ "$version" = latest ]; then
  base=https://github.com/$repo/releases/latest/download
else
  base=https://github.com/$repo/releases/download/$version
fi
asset=rig_${os}_${arch}.tar.gz

dir=${RIG_INSTALL_DIR:-}
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir=$HOME/.local/bin; fi
fi
mkdir -p "$dir"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "rig: downloading $asset ($version)"
curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

want=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "rig: checksum mismatch for $asset" >&2
  exit 1
fi

tar -xzf "$tmp/$asset" -C "$tmp" rig
install -m 0755 "$tmp/rig" "$dir/rig"
echo "rig: installed $("$dir/rig" --version 2>/dev/null || echo rig) to $dir/rig"

case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    # the login shell's own startup file; bash login shells (macOS terminals, ssh) skip .bashrc
    line="export PATH=\"$dir:\$PATH\""
    case "$(basename "${SHELL:-}")" in
      zsh) files="${ZDOTDIR:-$HOME}/.zshrc" ;;
      bash)
        files="$HOME/.bashrc"
        if [ "$(uname -s)" = Darwin ] || { [ -f "$HOME/.bash_profile" ] && ! grep -q bashrc "$HOME/.bash_profile"; }; then
          files="$files $HOME/.bash_profile"
        fi
        ;;
      fish) files="$HOME/.config/fish/config.fish" line="fish_add_path $dir" ;;
      *) files="$HOME/.profile" ;;
    esac
    for rc in $files; do
      grep -qs "$dir" "$rc" && continue
      mkdir -p "$(dirname "$rc")"
      printf '\n%s\n' "$line" >> "$rc"
      echo "rig: added $dir to PATH in $rc"
    done
    echo "  open a new terminal, or run: $line"
    ;;
esac

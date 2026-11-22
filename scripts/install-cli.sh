#!/usr/bin/env bash
# Build the pocketlab CLI and install it into the first writable directory
# that is on PATH: ~/.local/bin, then /usr/local/bin.
# Usage: scripts/install-cli.sh [--copy]   (default: symlink to bin/pocketlab)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mode=link
[ "${1:-}" = "--copy" ] && mode=copy

mkdir -p "$root/bin"
echo "building $root/bin/pocketlab"
(cd "$root" && go build -o bin/pocketlab ./cmd/pocketlab)

on_path() { case ":$PATH:" in *":$1:"*) return 0 ;; esac; return 1; }

dest=""
for d in "$HOME/.local/bin" /usr/local/bin; do
  if on_path "$d"; then
    if [ -d "$d" ] && [ -w "$d" ]; then dest="$d"; break; fi
    if [ ! -e "$d" ] && [ "$d" = "$HOME/.local/bin" ] && mkdir -p "$d" 2>/dev/null; then dest="$d"; break; fi
  fi
done

if [ -z "$dest" ]; then
  echo "no writable directory on PATH among ~/.local/bin and /usr/local/bin." >&2
  echo "binary is at $root/bin/pocketlab; add it to PATH or copy it manually." >&2
  exit 1
fi

# Pre-rename leftover: drop an old `phoneforge` symlink that pointed into this checkout.
old="$dest/phoneforge"
if [ -L "$old" ] && [ "$(readlink "$old")" = "$root/bin/phoneforge" ]; then
  rm -f "$old"; echo "removed old symlink $old"
fi

target="$dest/pocketlab"
if [ "$mode" = copy ]; then
  install -m 0755 "$root/bin/pocketlab" "$target"
  echo "copied bin/pocketlab -> $target"
else
  ln -sf "$root/bin/pocketlab" "$target"
  echo "symlinked $target -> $root/bin/pocketlab"
fi
echo "installed: $("$target" version)"

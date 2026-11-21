#!/usr/bin/env bash
# Build the phoneforge CLI and install it into the first writable directory
# that is on PATH: ~/.local/bin, then /usr/local/bin.
# Usage: scripts/install-cli.sh [--copy]   (default: symlink to bin/phoneforge)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mode=link
[ "${1:-}" = "--copy" ] && mode=copy

mkdir -p "$root/bin"
echo "building $root/bin/phoneforge"
(cd "$root" && go build -o bin/phoneforge ./cmd/phoneforge)

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
  echo "binary is at $root/bin/phoneforge; add it to PATH or copy it manually." >&2
  exit 1
fi

target="$dest/phoneforge"
if [ "$mode" = copy ]; then
  install -m 0755 "$root/bin/phoneforge" "$target"
  echo "copied bin/phoneforge -> $target"
else
  ln -sf "$root/bin/phoneforge" "$target"
  echo "symlinked $target -> $root/bin/phoneforge"
fi
echo "installed: $("$target" version)"

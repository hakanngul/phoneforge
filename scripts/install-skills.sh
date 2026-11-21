#!/usr/bin/env bash
# Install PhoneForge's Claude Code skills (skills/phoneforge-*) into ~/.claude/skills.
#
#   scripts/install-skills.sh              symlink each skill (updates with the repo)
#   scripts/install-skills.sh --copy       copy instead of symlink
#   scripts/install-skills.sh --uninstall  remove the PhoneForge skills this script installed
#   scripts/install-skills.sh --dest DIR   target directory (default: $CLAUDE_SKILLS_DIR or ~/.claude/skills)
#   scripts/install-skills.sh --dry-run    print what would happen, change nothing
#
# Only folders named phoneforge-* from this repo are touched. An existing entry with the same
# name is replaced only if it is a symlink or a copy of a PhoneForge skill (SKILL.md "name:" equals
# the folder name); anything else is left alone and reported.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
src_root="$repo/skills"
dest="${CLAUDE_SKILLS_DIR:-$HOME/.claude/skills}"
mode=link
action=install
dry=0

while [ $# -gt 0 ]; do
  case "$1" in
    --copy) mode=copy ;;
    --link) mode=link ;;
    --uninstall) action=uninstall ;;
    --dry-run) dry=1 ;;
    --dest) shift; dest="${1:?--dest needs a directory}" ;;
    -h|--help) sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

[ -d "$src_root" ] || { echo "no skills directory at $src_root" >&2; exit 1; }

run() { if [ "$dry" = 1 ]; then echo "  (dry-run) $*"; else "$@"; fi; }

skill_name() { # print the frontmatter name of a SKILL.md
  awk 'NR==1 && $0!="---"{exit} NR>1 && $0=="---"{exit} /^name:[[:space:]]*/{sub(/^name:[[:space:]]*/,""); print; exit}' "$1" 2>/dev/null || true
}

is_ours() { # $1 = existing path in dest, $2 = folder name
  local p="$1" n="$2"
  [ -L "$p" ] && return 0
  [ -d "$p" ] && [ "$(skill_name "$p/SKILL.md")" = "$n" ] && return 0
  return 1
}

echo "PhoneForge skills: $action ($mode) -> $dest"
[ "$dry" = 1 ] || mkdir -p "$dest"
count=0 skipped=0
for dir in "$src_root"/phoneforge-*/; do
  [ -f "$dir/SKILL.md" ] || continue
  name="$(basename "$dir")"
  target="$dest/$name"
  if [ "$action" = uninstall ]; then
    if [ -e "$target" ] || [ -L "$target" ]; then
      if is_ours "$target" "$name"; then
        echo "removed   $name"; run rm -rf "$target"; count=$((count+1))
      else
        echo "skipped   $name (not a PhoneForge skill, left in place)"; skipped=$((skipped+1))
      fi
    fi
    continue
  fi
  if [ -e "$target" ] || [ -L "$target" ]; then
    if is_ours "$target" "$name"; then
      run rm -rf "$target"
    else
      echo "skipped   $name ($target exists and is not a PhoneForge skill)"; skipped=$((skipped+1)); continue
    fi
  fi
  if [ "$mode" = copy ]; then
    run cp -R "${dir%/}" "$target"; echo "copied    $name"
  else
    run ln -s "${dir%/}" "$target"; echo "linked    $name -> ${dir%/}"
  fi
  count=$((count+1))
done
echo "done: $count $action, $skipped skipped"

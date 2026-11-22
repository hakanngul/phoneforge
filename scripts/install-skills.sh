#!/usr/bin/env bash
# Install Pocketlab's Claude Code skills (skills/pocketlab-*) into ~/.claude/skills.
#
#   scripts/install-skills.sh              symlink each skill (updates with the repo)
#   scripts/install-skills.sh --copy       copy instead of symlink
#   scripts/install-skills.sh --uninstall  remove the Pocketlab skills this script installed
#   scripts/install-skills.sh --dest DIR   target directory (default: $CLAUDE_SKILLS_DIR or ~/.claude/skills)
#   scripts/install-skills.sh --dry-run    print what would happen, change nothing
#
# Only folders named pocketlab-* from this repo are touched. An existing entry with the same
# name is replaced only if it is a symlink or a copy of a Pocketlab skill (SKILL.md "name:" equals
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

echo "Pocketlab skills: $action ($mode) -> $dest"
[ "$dry" = 1 ] || mkdir -p "$dest"
count=0 skipped=0
for dir in "$src_root"/pocketlab-*/; do
  [ -f "$dir/SKILL.md" ] || continue
  name="$(basename "$dir")"
  target="$dest/$name"
  if [ "$action" = uninstall ]; then
    if [ -e "$target" ] || [ -L "$target" ]; then
      if is_ours "$target" "$name"; then
        echo "removed   $name"; run rm -rf "$target"; count=$((count+1))
      else
        echo "skipped   $name (not a Pocketlab skill, left in place)"; skipped=$((skipped+1))
      fi
    fi
    continue
  fi
  if [ -e "$target" ] || [ -L "$target" ]; then
    if is_ours "$target" "$name"; then
      run rm -rf "$target"
    else
      echo "skipped   $name ($target exists and is not a Pocketlab skill)"; skipped=$((skipped+1)); continue
    fi
  fi
  if [ "$mode" = copy ]; then
    run cp -R "${dir%/}" "$target"; echo "copied    $name"
  else
    run ln -s "${dir%/}" "$target"; echo "linked    $name -> ${dir%/}"
  fi
  count=$((count+1))
done

# Pre-rename leftovers: skills were called phoneforge-* before the rebrand. Remove ones this script
# installed (a symlink into this repo's skills/ folder, or a copy whose SKILL.md name matches).
legacy=0
for target in "$dest"/phoneforge-*; do
  [ -e "$target" ] || [ -L "$target" ] || continue
  lname="$(basename "$target")"
  if { [ -L "$target" ] && case "$(readlink "$target")" in "$repo"/skills/*) true ;; *) false ;; esac; } \
     || { [ -d "$target" ] && [ ! -L "$target" ] && [ "$(skill_name "$target/SKILL.md")" = "$lname" ]; }; then
    echo "removed   $lname (renamed to pocketlab-${lname#phoneforge-})"; run rm -rf "$target"; legacy=$((legacy+1))
  fi
done
[ "$legacy" = 0 ] || echo "removed $legacy pre-rename phoneforge-* skills"
echo "done: $count $action, $skipped skipped"

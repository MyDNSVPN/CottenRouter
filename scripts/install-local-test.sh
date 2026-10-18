#!/usr/bin/env bash
set -euo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
for name in fail validate_local_source stage_local_source; do
  eval "$(awk -v name="$name" '$0 ~ "^" name "\\(\\) \\{" {found=1} found {print} found && /^}/ {exit}' "$script_dir/install.sh")"
done
source_dir="$fixture/checkout with spaces"
mkdir -p "$source_dir"/{cmd/cottenrouter,internal,packaging,scripts,.cache,.git}
for file in go.mod go.sum cottenrouter.bootstrap.json packaging/cottenrouter.service scripts/install.sh scripts/ensure-swap.sh scripts/uninstall.sh scripts/go-checksums.txt cmd/cottenrouter/main.go; do
  printf 'current uncommitted content\r\n' > "$source_dir/$file"
done
printf 'uncommitted Go source\n' > "$source_dir/internal/new.go"
printf 'local secret\n' > "$source_dir/.cache/private"
cp "$source_dir/scripts/uninstall.sh" "$fixture/original-uninstall.sh"
stage_local_source "$source_dir" "$fixture/staged"
cmp "$source_dir/internal/new.go" "$fixture/staged/internal/new.go"
[[ ! -e $fixture/staged/.cache && ! -e $fixture/staged/.git ]]
[[ $(<"$fixture/staged/scripts/uninstall.sh") == 'current uncommitted content' ]]
cmp "$source_dir/scripts/uninstall.sh" "$fixture/original-uninstall.sh"

# An inspection error must fail closed, even when find printed no link name.
if (
  find() { return 23; }
  stage_local_source "$source_dir" "$fixture/unreadable"
) >/dev/null 2>&1; then
  echo 'FAIL: local-source ignored a failed path inspection' >&2; exit 1
fi
[[ ! -e $fixture/unreadable ]]

# Required bootstrap helpers and checksum data must exist before copying.
for file in go.mod scripts/install.sh scripts/go-checksums.txt; do
  mv "$source_dir/$file" "$fixture/removed"
  if (stage_local_source "$source_dir" "$fixture/missing") >/dev/null 2>&1; then
    echo "FAIL: local checkout without $file was accepted" >&2; exit 1
  fi
  [[ ! -e $fixture/missing ]]
  mv "$fixture/removed" "$source_dir/$file"
done

# Simulate a required file disappearing during copy. A pre-copy check alone
# cannot detect that race, so the staged tree must be checked before building.
if (
  cp() {
    command cp "$@" || return
    if [[ $1 == -R && $3 == "$source_dir/scripts" ]]; then
      rm "$fixture/changed-during-copy/scripts/go-checksums.txt"
    fi
  }
  stage_local_source "$source_dir" "$fixture/changed-during-copy"
) >/dev/null 2>&1; then
  echo 'FAIL: local-source did not revalidate copied inputs' >&2; exit 1
fi

# Native symlinks are unavailable on some Windows Git Bash installations.
# Linux CI additionally exercises both a linked source file and ancestor.
mkdir "$fixture/outside"
if ln -s "$fixture/outside" "$source_dir/internal/linked.go" 2>/dev/null && [[ -L $source_dir/internal/linked.go ]]; then
  if (stage_local_source "$source_dir" "$fixture/rejected") >/dev/null 2>&1; then
    echo 'FAIL: local-source accepted a symlinked build input' >&2; exit 1
  fi
  [[ ! -e $fixture/rejected ]]
  rm "$source_dir/internal/linked.go"
  mv "$source_dir/cmd/cottenrouter" "$fixture/real-cmd"
  ln -s "$fixture/real-cmd" "$source_dir/cmd/cottenrouter"
  if (stage_local_source "$source_dir" "$fixture/linked-ancestor") >/dev/null 2>&1; then
    echo 'FAIL: local-source accepted a symlinked command directory' >&2; exit 1
  fi
  [[ ! -e $fixture/linked-ancestor ]]
else
  echo 'Native symlink cases require Linux or Windows symlink support.'
fi
echo 'install.sh current-checkout staging OK'

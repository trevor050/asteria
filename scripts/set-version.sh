#!/usr/bin/env bash
# Stamp a version (e.g. 0.2.0) into every build asset that carries one.
# Used by the release workflow with the pushed tag; safe to run locally.
set -euo pipefail
cd "$(dirname "$0")/.."

new="${1#v}"
if [[ ! "$new" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: $0 <version>   e.g. $0 0.2.0" >&2
  exit 1
fi
old=$(sed -n 's/^  version: "\(.*\)".*/\1/p' build/config.yml)
[ -n "$old" ] || { echo "could not read the current version from build/config.yml" >&2; exit 1; }

files=(
  build/config.yml
  build/linux/nfpm/nfpm.yaml
  build/darwin/Info.plist
  build/darwin/Info.dev.plist
  build/windows/info.json
  build/windows/wails.exe.manifest
  build/windows/nsis/wails_tools.nsh
  version.go
)
for f in "${files[@]}"; do
  perl -pi -e "s/\Q$old\E/$new/g" "$f"
done
echo "version $old -> $new"

#!/usr/bin/env bash
# Stamp a release version into the generated build assets: ./build/set-version.sh 1.2.3
# (`wails3 task common:update:build-assets` would do it too, but it also rewrites hand-edited files like nfpm.yaml.)
set -euo pipefail
cd "$(dirname "$0")"
new="$1"
old=$(sed -n 's/^  version: "\(.*\)".*/\1/p' config.yml)
[ -n "$old" ] || { echo "no version in build/config.yml" >&2; exit 1; }
[ "$old" = "$new" ] && exit 0
files=(config.yml darwin/Info.plist darwin/Info.dev.plist linux/nfpm/nfpm.yaml windows/info.json
       windows/nsis/wails_tools.nsh windows/wails.exe.manifest windows/msix/app_manifest.xml windows/msix/template.xml)
for f in "${files[@]}"; do
  perl -pi -e "s/\\Q$old\\E/$new/g" "$f"
done
echo "version $old -> $new"

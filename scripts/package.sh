#!/bin/sh
# Builds out/Trickle.zip for "Install plugin from ZIP" in Decky's developer settings.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
stage="$root/out/Trickle"

make -C "$root/backend"
(cd "$root" && npx --yes pnpm@9 install --frozen-lockfile && npx --yes pnpm@9 run build)

rm -rf "$root/out"
mkdir -p "$stage/bin" "$stage/dist"
cp "$root/backend/out/torrentd" "$stage/bin/"
cp "$root/dist/index.js" "$stage/dist/"
cp "$root/main.py" "$root/plugin.json" "$root/package.json" "$root/LICENSE" "$root/README.md" "$stage/"

(cd "$root/out" && zip -qr Trickle.zip Trickle)
echo "$root/out/Trickle.zip"

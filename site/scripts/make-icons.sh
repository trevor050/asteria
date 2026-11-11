#!/bin/sh
# Builds the favicons from the app icon (build/appicon.svg / .png). Output is committed.
set -e
cd "$(dirname "$0")/.."
cp ../build/appicon.svg public/favicon.svg
sips -z 180 180 ../build/appicon.png --out public/apple-touch-icon.png >/dev/null
sips -z 64 64 ../build/appicon.png --out src/assets/icon-64.png >/dev/null
sips -z 32 32 ../build/appicon.png --out public/favicon-32.png >/dev/null
ls -l public src/assets

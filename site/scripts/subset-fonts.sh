#!/bin/sh
# Subsets the app's Geist variable font (frontend/src/assets/fonts, OFL) to the
# characters the site uses. Run after changing the font; the output is committed.
# Needs fontTools with brotli: pip install fonttools brotli
set -e
cd "$(dirname "$0")/.."
pyftsubset ../frontend/src/assets/fonts/Geist-Variable.woff2 \
  --unicodes="U+0020-007E,U+00A0-00FF,U+2010-2015,U+2018-201E,U+2022,U+2026,U+2032-2033,U+2190-2193,U+21B5,U+2212,U+2318,U+2325,U+21E7" \
  --layout-features='kern,liga,calt,tnum,case,ss01' \
  --flavor=woff2 \
  --output-file=src/assets/fonts/Geist-Variable-subset.woff2
cp ../frontend/src/assets/fonts/OFL.txt src/assets/fonts/OFL.txt
ls -l src/assets/fonts

#!/usr/bin/env bash
# Builds the small fixtures in testdata/formats that need an engine to
# create (Office documents, ebooks, PostScript, archives, long-tail raster).
# Each carries the marker text "Asteria matrix check" where the format can
# hold text, and is made with a DIFFERENT engine from the one that converts
# it in the format matrix where possible, so a conversion can't pass by
# reading back its own quirks. Run on Linux with: pandoc, libreoffice,
# calibre (ebook-convert), imagemagick, ghostscript, poppler-utils, 7zip,
# ffmpeg, python3-fonttools, python3-brotli (and pyarrow for Parquet).
# Outputs are committed; rerun only to refresh them.
set -euo pipefail
cd "$(dirname "$0")/../../testdata/formats"
M="Asteria matrix check"
tmp=$(mktemp -d)
SZ=$(command -v 7zz || command -v 7z || command -v 7za)
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/doc.md" <<EOF
# Sample

$M. A second paragraph with **bold** and *italic* text.

| name | value |
| ---- | ----- |
| a    | 1     |
EOF

# Word-processing documents: pandoc writes docx/odt/rtf/epub/html/rst/tex.
pandoc "$tmp/doc.md" -o sample.docx
pandoc "$tmp/doc.md" -o sample.odt
pandoc "$tmp/doc.md" -s -o sample.rtf
pandoc "$tmp/doc.md" -o sample.epub --metadata title="Sample"
pandoc "$tmp/doc.md" -t rst -o sample.rst
pandoc "$tmp/doc.md" -s -t latex -o sample.tex
pandoc "$tmp/doc.md" -t org -o sample.org
pandoc "$tmp/doc.md" -t textile -o sample.textile
pandoc "$tmp/doc.md" -s -o sample.html
pandoc "$tmp/doc.md" -o sample.pptx
pandoc "$tmp/doc.md" -t fb2 -o sample.fb2
# Notebook with the marker in a markdown cell.
printf '{"cells":[{"cell_type":"markdown","metadata":{},"source":["%s"]}],"metadata":{},"nbformat":4,"nbformat_minor":5}\n' "$M" > sample.ipynb

# Legacy binary Office formats and spreadsheets: LibreOffice from the above.
prof="file://$tmp/lo-profile"
lo() { soffice -env:UserInstallation="$prof" --headless --convert-to "$1" --outdir "$tmp" "$2" >/dev/null 2>&1; }
lo doc sample.docx && mv "$tmp/sample.doc" .
lo ppt sample.pptx && mv "$tmp/sample.ppt" .
lo odp sample.pptx && mv "$tmp/sample.odp" .
printf 'name,note\nfirst,%s\nsecond,two\n' "$M" > "$tmp/sheet.csv"
lo xlsx "$tmp/sheet.csv" && mv "$tmp/sheet.xlsx" sample.xlsx
lo xls "$tmp/sheet.csv" && mv "$tmp/sheet.xls" sample.xls
lo ods "$tmp/sheet.csv" && mv "$tmp/sheet.ods" sample.ods

# Ebooks: Calibre from the pandoc EPUB.
ebook-convert sample.epub sample.mobi >/dev/null
ebook-convert sample.epub sample.azw3 >/dev/null

# PostScript: Ghostscript from a PDF made by LibreOffice.
lo pdf sample.docx && gs -q -dSAFER -dBATCH -dNOPAUSE -sDEVICE=ps2write -sOutputFile=sample.ps "$tmp/sample.pdf"
gs -q -dSAFER -dBATCH -dNOPAUSE -sDEVICE=eps2write -sOutputFile=sample.eps "$tmp/sample.pdf"

# Archives: 7-Zip (7z) and genisoimage/xorriso if present (iso).
mkdir -p "$tmp/arch" && echo "$M" > "$tmp/arch/note.txt"
rm -f sample.7z && (cd "$tmp/arch" && "$SZ" a -t7z "$OLDPWD/sample.7z" note.txt >/dev/null)
rm -f sample.cab sample.wim
(cd "$tmp/arch" && "$SZ" a -twim "$OLDPWD/sample.wim" note.txt >/dev/null) || true

# Long-tail raster: ImageMagick from a known picture.
convert -size 96x64 gradient:'#1f3f4d-#e0a85e' "$tmp/base.png"
for ext in tif bmp psd tga pcx ico jp2 dds xpm ppm pgm pbm sgi ras; do
  convert "$tmp/base.png" "sample.$ext" 2>/dev/null || echo "skip $ext"
done
convert "$tmp/base.png" -quality 60 sample.avif 2>/dev/null || echo "skip avif"

# Fonts: fontTools (python3-fonttools, python3-brotli) builds a small CFF
# OpenType font from scratch and the WOFF/WOFF2 flavours of the TrueType
# fixture, independently of Asteria's own font code.
python3 - <<'PY'
from fontTools.fontBuilder import FontBuilder
from fontTools.pens.t2CharStringPen import T2CharStringPen
from fontTools.ttLib import TTFont
fb = FontBuilder(1000, isTTF=False)
fb.setupGlyphOrder([".notdef", "A"])
fb.setupCharacterMap({0x41: "A"})
def box():
    pen = T2CharStringPen(600, None)
    pen.moveTo((50, 0)); pen.lineTo((550, 0)); pen.lineTo((300, 700)); pen.closePath()
    return pen.getCharString()
fb.setupCFF("AsteriaSample", {"FullName": "Asteria Sample"}, {".notdef": box(), "A": box()}, {})
fb.setupHorizontalMetrics({".notdef": (600, 50), "A": (600, 50)})
fb.setupHorizontalHeader(ascent=800, descent=-200)
fb.setupNameTable({"familyName": "Asteria Sample", "styleName": "Regular"})
fb.setupOS2(); fb.setupPost()
fb.save("sample.otf")
for flavor in ("woff", "woff2"):
    f = TTFont("sample.ttf"); f.flavor = flavor; f.save("sample." + flavor)
    # CFF-outline web fonts for the OTF conversion.
    f = TTFont("sample.otf"); f.flavor = flavor; f.save("convert_to_otf." + flavor)
PY

# Parquet: pyarrow if present (pip install pyarrow in a venv).
python3 -c "import pyarrow as pa, pyarrow.parquet as pq; pq.write_table(pa.table({'name': ['first', 'second'], 'note': ['$M', 'two']}), 'sample.parquet')" || echo "skip parquet"

# A picture with the marker for OCR is made by the Mac script (fonts differ).
# Audio and video samples for the wider ffmpeg skills.
ffmpeg -v error -y -f lavfi -i "sine=frequency=440:duration=2" -c:a libvorbis sample.ogg
ffmpeg -v error -y -f lavfi -i "sine=frequency=440:duration=2" -c:a libopus sample.opus
ffmpeg -v error -y -f lavfi -i "sine=frequency=440:duration=2" sample.aiff
ffmpeg -v error -y -f lavfi -i "testsrc2=s=160x120:d=2" -f lavfi -i "sine=d=2" -shortest -c:v libvpx-vp9 -c:a libopus sample.webm
ffmpeg -v error -y -f lavfi -i "testsrc2=s=160x120:d=2" -f lavfi -i "sine=d=2" -shortest sample.mkv
ffmpeg -v error -y -f lavfi -i "testsrc2=s=160x120:d=2" -f lavfi -i "sine=d=2" -shortest -c:v mpeg4 sample.avi
ffmpeg -v error -y -f lavfi -i "testsrc2=s=160x120:d=2" -f lavfi -i "sine=d=2" -shortest sample.mov
ls -la
